package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	cts "github.com/cvanderschuere/go-connexionz"
)

/*
  /arrivals (endpoint to access arrival information)

  Default: nothing returned
  Paramaters:
    stops:comma delimited list of stop numbers (required); Default: ""
    date: date in RFC822Z format; Default: "currentDate"

  Response:
    stops: map stopNumber to array of arrival times in RFC822Z


*/
func Arrivals(c appengine.Context, w http.ResponseWriter, r *http.Request) {
	// Make sure this is a GET request
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	sepStops := strings.Split(r.FormValue("stops"), ",")
	if len(sepStops) == 0 || sepStops[0] == "" {
		http.Error(w, "Missing required paramater: stops", 400)
		return
	}

	// Use date input if avaliable
	var filterTime time.Time
	loc, _ := time.LoadLocation("America/Los_Angeles")
	currentTime := time.Now().In(loc) // Must account for time zone
	paramDate := r.FormValue("date")
	if len(paramDate) != 0 {
		//Parse Date
		inputTime, timeErr := time.Parse(time.RFC822Z, paramDate)
		if timeErr != nil {
			http.Error(w, "Paramater Error[date]: "+timeErr.Error(), 400)
			return
		}

		filterTime = inputTime.In(loc) // Using time from parameter
	} else {
		filterTime = currentTime
	}

	// Calculate duration since midnight
	hour, min, sec := filterTime.Clock()
	durationSinceMidnight := time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec)*time.Second

	// Used to indicated CTS call finished
	finished := make(chan error, 1)
	stopIDToExpectedTime := make(map[int64]([]ETA))

	// Only make CTS request if asking for within 30 min in the future -- CTS restriction
	if diff := filterTime.Sub(currentTime); diff >= 0 && diff < 30*time.Minute {
		// Request CTS realtime information concurrently

		go func(c appengine.Context, stopNumStrings []string, m map[int64]([]ETA), finChan chan<- error) {
			client := cts.New(c, baseURL)

			var wg sync.WaitGroup

			// Loop over stops and make arrival calls for each
			for _, stopNumString := range stopNumStrings {
				wg.Add(1)

				go func(numString string) {
					defer wg.Done()

					stopNum, _ := strconv.ParseInt(numString, 10, 64)

					plat := &cts.Platform{Number: stopNum}

					// Make CTS call
					ctsRoutes, _ := client.ETA(plat)

					ctsEstimates := []ETA{}
					for _, ctsRoute := range ctsRoutes {
						if len(ctsRoute.Destination) > 0 {
							if ctsRoute.Destination[0].Trip != nil {
								// This stop+route as valid ETA
								estDur := time.Duration(ctsRoute.Destination[0].Trip.ETA) * time.Minute

								// Create new eta
								e := ETA{
									route:    ctsRoute.Number,
									expected: estDur,
								}
								ctsEstimates = append(ctsEstimates, e)
							}
						}
					}

					m[stopNum] = ctsEstimates
				}(stopNumString)
			}

			wg.Wait()
			finished <- nil

		}(c, sepStops, stopIDToExpectedTime, finished)
	} else {
		finished <- nil
		close(finished)
	}

	var wg sync.WaitGroup
	locker := new(sync.Mutex)

	// Load all arrival information from datastore for given stop
	arrivals := make(map[int64]([]*Arrival))
	for _, stopString := range sepStops {
		wg.Add(1)

		go func(stopNumString string) {
			defer wg.Done()

			var dest []*Arrival
			stopNum, _ := strconv.ParseInt(stopNumString, 10, 64)

			// Check memcache
			cacheName := "arrivalCache:" + stopNumString + ":" + filterTime.Weekday().String()
			if _, memError := memcache.Gob.Get(c, cacheName, &dest); memError == memcache.ErrCacheMiss {
				// Repopulate memcache
				// Query for arrival
				parent := datastore.NewKey(c, "Stop", "", stopNum, nil)
				q := datastore.NewQuery("Arrival").Ancestor(parent)
				//q = q.Filter("Scheduled >=", durationSinceMidnight)
				q = q.Filter(filterTime.Weekday().String()+" =", true)
				q = q.Order("Scheduled")

				_, getError := q.GetAll(c, &dest)
				if getError != nil {
					http.Error(w, "Arrival Error: "+getError.Error(), 500)
					return //Try next loop -- bad stopNum
				}

				// Add to memcache
				item := &memcache.Item{
					Key:    cacheName,
					Object: dest,
				}

				go memcache.Gob.Set(c, item) // We do enough work below that this will finish

				// Filter based on filterTime
				filtered := filterArrivalsOnTime(durationSinceMidnight, dest)
				locker.Lock()
				arrivals[stopNum] = filtered
				locker.Unlock()

			} else if memError == memcache.ErrServerError {
				http.Error(w, "Get Arrivals Error: "+memError.Error(), 500)
			} else {
				// Filter based on filterTime
				filtered := filterArrivalsOnTime(durationSinceMidnight, dest)
				locker.Lock()
				arrivals[stopNum] = filtered
				locker.Unlock()
			}
		}(stopString)
	}

	// Wait until all database calls are done
	wg.Wait()

	// Wait until cts call finishes
	<-finished

	// Create maps with given information
	output := make(map[string][](map[string]string))
	for stopNum, vals := range arrivals {
		etas := stopIDToExpectedTime[stopNum]

		// Check whether we should drop the first scheduled arrival or not
		// Bias towards buses being late -- expected is not more than a loop late
		if len(etas) > 0 && len(vals) > 0 && durationSinceMidnight+etas[0].expected < (vals[0].Scheduled+50*time.Minute) {
			// Within 50 minutes late ... don't drop
		} else if len(vals) > 0 {
			vals = vals[1:]
		}

		if len(etas) > len(vals) {
			c.Infof("Added %d etas to stop %s", len(etas)-len(vals), stopNum)

			// Add additional etas to scheduled times
			l := len(vals)
			for _, eta := range etas[l:] {
				// Make new arrival
				r := &Arrival{
					routeName: eta.route,
					Scheduled: eta.expected,
				}
				c.Debugf("Appending:", eta, r)

				vals = append(vals, r)
			}
		}

		result := make([](map[string]string), len(vals))

		// Loop over arrival array
		for i, val := range vals {
			rName := val.routeName

			if rName == "" {
				// Get route
				var route Route
				datastore.Get(c, val.Route, &route)
				rName = route.Name
			}

			// Use this arrival to determine information
			scheduled := val.Scheduled
			expected := scheduled

			if len(etas) > 0 {
				// Add eta offset
				expected = durationSinceMidnight + etas[0].expected
				etas = etas[1:] // Skip to next one
			}

			//Convert to times
			midnight := time.Date(filterTime.Year(), filterTime.Month(), filterTime.Day(), 0, 0, 0, 0, loc)
			scheduledTime := midnight.Add(scheduled)
			expectedTime := midnight.Add(expected)

			m := map[string]string{
				"Route":     rName,
				"Scheduled": scheduledTime.Format(time.RFC822Z),
				"Expected":  expectedTime.Format(time.RFC822Z),
			}

			result[i] = m
		}

		output[strconv.FormatInt(stopNum, 10)] = result
	}

	// Output JSON
	data, errJSON := json.Marshal(output)
	if errJSON != nil {
		http.Error(w, errJSON.Error(), 500)
		return
	}

	// Output JSON
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, string(data))
}

func filterArrivalsOnTime(dur time.Duration, arrivals []*Arrival) []*Arrival {
	var i int
	for i = 0; i < len(arrivals); i++ {
		if arrivals[i].Scheduled >= dur {
			break
		}
	}

	if i == 0 {
		return arrivals
	} else {
		return arrivals[i-1:] // Return one more than required
	}
}
