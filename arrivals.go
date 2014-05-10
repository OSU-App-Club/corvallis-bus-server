package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	cts "github.com/cvanderschuere/go-connexionz"
)

//
// Memory Cache
//
var cachedArrivals map[int64]([]*Arrival)
var curDay string
var arrivalCacheLock *sync.RWMutex
var globalRouteNames map[int64]string

func init() {
	globalRouteNames = make(map[int64]string)
	cachedArrivals = make(map[int64]([]*Arrival))
	arrivalCacheLock = new(sync.RWMutex)
}

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
	c.Debugf("Stops:", sepStops)
	if len(sepStops) == 0 || sepStops[0] == "" {
		http.Error(w, "Missing required paramater: stops", 400)
		return
	} else if len(sepStops) > 20 {
		http.Error(w, "Maximum of 20 stops exceeded", 400)
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

	// Determine if arrivals could be avaliable
	diff := filterTime.Sub(currentTime)
	checkCTS := diff >= 0 && diff < 30*time.Minute

	// Load arrivals from datastore add/or CTS
	var wg sync.WaitGroup
	locker := new(sync.Mutex)
	output := make(map[string]([]map[string]string))
	for _, stopID := range sepStops {
		wg.Add(1)

		go func(s string) {
			defer wg.Done()
			stopNum, _ := strconv.ParseInt(s, 10, 64)
			stopArrivals := findArrivalsForStop(c, stopNum, checkCTS, &filterTime)

			// Add to map -- mutex protected
			locker.Lock()
			output[s] = stopArrivals
			locker.Unlock()
		}(stopID)
	}

	// Wait for all stops to finish
	wg.Wait()

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

func findArrivalsForStop(c appengine.Context, stopNum int64, checkCTS bool, filterTime *time.Time) []map[string]string {

	// Realtime
	realtimeETAs := make(chan []*ETA, 1)
	if checkCTS {
		go getRealtimeArrivals(c, stopNum, filterTime, realtimeETAs)
	} else {
		realtimeETAs <- nil
		close(realtimeETAs)
	}

	// Schedule
	scheduledArrivals := make(chan []*Arrival, 1)
	getArrivalsFromDatastore(c, stopNum, filterTime, scheduledArrivals)

	// Sync point -- make sure all data is known
	scheds := <-scheduledArrivals
	etas := <-realtimeETAs

	// We need to combine eta and sched -- typical case
	if len(etas) > 0 && len(scheds) > 1 {
		// expected should be between the two scheduled
		if etas[0].expected >= scheds[1].Scheduled {
			scheds = scheds[1:]
		}
	} else if len(etas) > 0 && len(scheds) == 1 {
		// Match them up regardless of time
	} else if len(scheds) > 0 {
		// no eta -- include schedule after current time
		scheds = scheds[1:]
	}

	// Check if we should add extra etas
	if len(etas) > len(scheds) {
		l := len(scheds) // Save here because vals is growing
		for _, eta := range etas[l:] {
			r := &Arrival{
				routeName: eta.route,
				Scheduled: eta.expected,
			}
			scheds = append(scheds, r)
		}
	}

	// Create arrivals
	var wg sync.WaitGroup
	arrivalOutput := make([](map[string]string), len(scheds))
	for i, arr := range scheds {
		wg.Add(1)
		go func(j int, a *Arrival) {
			defer wg.Done()
			var o map[string]string
			if len(etas) > j {
				o = prepareArrivalOutput(c, a, etas[j], filterTime)
			} else {
				o = prepareArrivalOutput(c, a, nil, filterTime)
			}
			arrivalOutput[j] = o // Concurrent write
		}(i, arr)
	}

	wg.Wait() // Wait until each is finished
	return arrivalOutput
}

// Fetch realtime info from connexionz
func getRealtimeArrivals(c appengine.Context, stopNum int64, filterTime *time.Time, etaChan chan []*ETA) {

	client := cts.New(c, baseURL)
	plat := &cts.Platform{Number: stopNum}

	// Make CTS call
	ctsRoutes, _ := client.ETA(plat)

	hour, min, sec := filterTime.Clock()
	durationSinceMidnight := time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec)*time.Second

	ctsEstimates := []*ETA{}
	for _, ctsRoute := range ctsRoutes {
		if len(ctsRoute.Destination) > 0 {
			if ctsRoute.Destination[0].Trip != nil {
				// This stop+route as valid ETA
				estDur := time.Duration(ctsRoute.Destination[0].Trip.ETA) * time.Minute

				// Create new eta
				e := &ETA{
					route:    ctsRoute.Number,
					expected: durationSinceMidnight + estDur,
				}
				ctsEstimates = append(ctsEstimates, e)
			}
		}
	}

	etaChan <- ctsEstimates
	close(etaChan)
}

func getArrivalsFromDatastore(c appengine.Context, stopNum int64, filterTime *time.Time, arrivalChan chan []*Arrival) {
	// Calc duration since midnight
	hour, min, sec := filterTime.Clock()
	durationSinceMidnight := time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec)*time.Second

	// Check memory cache
	arrivalCacheLock.RLock() // Read lock's only contention during inital write
	cachedDest, ok := cachedArrivals[stopNum]
	savedDay := curDay
	arrivalCacheLock.RUnlock()

	loc, _ := time.LoadLocation("America/Los_Angeles")
	now := time.Now().In(loc)

	// Will update every day
	if ok && savedDay == time.Now().Weekday().String() {
		// Filter based on filterTime
		arrivalChan <- filterArrivalsOnTime(durationSinceMidnight, cachedDest)
	} else {
		c.Infof("Repopulate for %d: %s", stopNum, filterTime.Weekday().String(), ok, savedDay)
		// Repopulate cache
		// Query for arrival
		dest := []*Arrival{}
		parent := datastore.NewKey(c, "Stop", "", stopNum, nil)
		q := datastore.NewQuery("Arrival").Ancestor(parent)
		q = q.Filter(filterTime.Weekday().String()+" =", true)
		q = q.Order("Scheduled")

		_, getError := q.GetAll(c, &dest)
		if getError != nil {
			arrivalChan <- nil
			return // Probably invalid stop num
		}

		// Save in memory -- only if today
		if savedDay != now.Weekday().String() {
			arrivalCacheLock.Lock() // Lock for writting

			curDay = now.Weekday().String()               // New day
			cachedArrivals = make(map[int64]([]*Arrival)) // clear
			cachedArrivals[stopNum] = dest

			arrivalCacheLock.Unlock() // Unlock for writting
		} else if savedDay == filterTime.Weekday().String() {
			arrivalCacheLock.Lock() // Lock for writting
			cachedArrivals[stopNum] = dest
			arrivalCacheLock.Unlock() // Unlock for writting
		}
		// Don't update cache for days other than today

		// Filter based on filterTime
		arrivalChan <- filterArrivalsOnTime(durationSinceMidnight, dest)
	}
	close(arrivalChan)
}

func prepareArrivalOutput(c appengine.Context, val *Arrival, eta *ETA, filterTime *time.Time) map[string]string {

	// Get route name -- might take a while so send result on channel
	nameChan := make(chan string, 1)
	go func(arr *Arrival) {
		if arr.routeName == "" {
			// Get route -- try global first
			if savedName, ok := globalRouteNames[val.Route.IntID()]; !ok {
				var route Route
				datastore.Get(c, val.Route, &route)
				nameChan <- route.Name
				globalRouteNames[val.Route.IntID()] = route.Name
			} else {
				nameChan <- savedName
			}
		} else {
			nameChan <- arr.routeName
		}
	}(val)

	// Use this arrival to determine information
	scheduled := val.Scheduled
	expected := scheduled

	// Realtime update
	if eta != nil {
		// Add eta offset
		expected = eta.expected
	}

	loc, _ := time.LoadLocation("America/Los_Angeles")

	//Convert to times
	midnight := time.Date(filterTime.Year(), filterTime.Month(), filterTime.Day(), 0, 0, 0, 0, loc)
	scheduledTime := midnight.Add(scheduled)
	expectedTime := midnight.Add(expected)

	m := map[string]string{
		"Route":     <-nameChan,
		"Scheduled": scheduledTime.Format(time.RFC822Z),
		"Expected":  expectedTime.Format(time.RFC822Z),
	}

	return m
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
