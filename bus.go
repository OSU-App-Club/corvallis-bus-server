package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cts "github.com/cvanderschuere/go-connexionz"
	"github.com/kellydunn/golang-geo"
	"github.com/pierrre/geohash"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	// New API
	http.HandleFunc("/routes", Routes)
	http.HandleFunc("/stops", Stops)
	http.HandleFunc("/arrivals", Arrivals)
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
func Arrivals(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

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

/*
	/routes (endpoint to access route information)

	Default: returns all routes without detailed stops
	Paramaters:
		names: comma delimited list of route names (optional); Default: ""
		stops: include stop information ["true" or "false"]; Default: "false"
		onlyNames: only include route names ["true" or "false"]; Default: "false"

	Response:
		routes: array of route objects

*/
func Routes(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	// Make sure this is a GET request
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	q := datastore.NewQuery("Route").Order("Name")
	cacheName := "allRoutes"

	// Check if should only return only only names
	if strings.ToLower(r.FormValue("onlyNames")) == "true" {
		cacheName += "OnlyName"
	}

	var routes []*Route

	// Try to get from memcache
	if _, memError := memcache.Gob.Get(c, cacheName, &routes); memError == memcache.ErrCacheMiss {
		// Load from datastore
		_, err := q.GetAll(c, &routes)

		c.Debugf("Result[%d]: ", len(routes), routes)

		if err != nil {
			http.Error(w, "Get Routes Error: "+err.Error(), 500)
			return
		}

		// Clear unneed info
		if strings.ToLower(r.FormValue("onlyNames")) == "true" {
			for _, route := range routes {
				route.AdditionalName = ""
				route.Description = ""
				route.URL = ""
				route.Polyline = ""
				route.Color = ""
				route.Direction = ""
				route.Start = time.Time{}
				route.End = time.Time{}
			}
		}

		// Save in memcache
		item := &memcache.Item{
			Key:    cacheName,
			Object: routes,
		}

		go memcache.Gob.Set(c, item)
	} else if memError == memcache.ErrServerError {
		http.Error(w, "Get Routes Error: "+memError.Error(), 500)
	}

	// Filter based on "names" param
	routesFilter := r.FormValue("names")
	if len(routesFilter) != 0 {
		//Comma seperated
		sepNames := strings.Split(routesFilter, ",")

		//Sort by name -- matching query
		sort.Sort(sort.StringSlice(sepNames))

		newRoutes := make([]*Route, len(sepNames))

		for i, routeName := range sepNames {
			for _, route := range routes {
				if route.Name == routeName {
					newRoutes[i] = route
				}
			}
		}

		routes = newRoutes
	}

	// Load stops
	if strings.ToLower(r.FormValue("stops")) == "true" {
		for _, route := range routes {
			path := make([]*Stop, len(route.Stops))
			for i, _ := range path {
				path[i] = new(Stop)
			}

			// Check memcache
			if _, memError := memcache.Gob.Get(c, route.Name+"-Path", &path); memError == memcache.ErrCacheMiss {
				datastore.GetMulti(c, route.Stops, path)

				// Populate IDs
				for j, stop := range path {
					stop.ID = route.Stops[j].IntID()
				}

				route.Path = path

				// Add to memcache
				item := &memcache.Item{
					Key:    route.Name + "-Path",
					Object: path,
				}
				memcache.Gob.Set(c, item)

			} else if memError == memcache.ErrServerError {
				http.Error(w, "Get Path Error: "+memError.Error(), 500)
			} else {
				// Use memcache value
				route.Path = path
			}
		}
	}

	result := map[string]interface{}{
		"routes": routes,
	}

	//Print out the pattern information
	data, errJSON := json.Marshal(result)
	if errJSON != nil {
		http.Error(w, errJSON.Error(), 500)
		return
	}

	// Output JSON
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, string(data))
}

/*
	/stops (endpoint to access stop information)

	Default: returns all stops
	Paramaters:
		ids: comma delimited list of stop ids (optional); Default: ""
		lat: latitude for search; Default: ""
		lng: longitude for search; Default: ""
		radius: radius in meters to make search; Default: 500
		limit: limit the amount of stops returned; Default: none
	Response:
		stops: array of stops objects
			-- Sort order different based on paramaters
				-- location: sorted by distance
				-- ids: sorted by ids

*/
func Stops(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	// Determing if need to search based on location or use stopNumbers
	var stops []*Stop
	var err error

	if r.FormValue("ids") != "" {
		// Split on commas
		sepIDs := strings.Split(r.FormValue("ids"), ",")

		// Make keys
		keys := make([]*datastore.Key, len(sepIDs))
		actualCount := 0
		for _, id := range sepIDs {
			idVal, idErr := strconv.ParseInt(id, 10, 64)
			if idErr == nil {
				keys[actualCount] = datastore.NewKey(c, "Stop", "", idVal, nil)
				actualCount++
			}
		}

		keys = keys[:actualCount]

		if len(keys) > 0 {
			// Do a search by IDs
			stops = make([]*Stop, len(keys))
			for i, _ := range stops {
				stops[i] = new(Stop)
			}
			datastore.GetMulti(c, keys, stops)

			// Populate IDs
			for i, stop := range stops {
				stop.ID = keys[i].IntID()
			}
		}

	} else if r.FormValue("lat") != "" && r.FormValue("lng") != "" {
		// Do a radius search
		lat, latErr := strconv.ParseFloat(r.FormValue("lat"), 64)
		lng, lngErr := strconv.ParseFloat(r.FormValue("lng"), 64)

		if latErr == nil && lngErr == nil {
			// Determine radius to use
			radius := 500
			if r.FormValue("radius") != "" {
				val, radErr := strconv.Atoi(r.FormValue("radius"))
				if radErr == nil {
					radius = val
				}
			}

			stops, err = stopsInRadius(c, lat, lng, radius)

		} else {
			err = errors.New("Error in parsing Latitude and Longitude")
		}

	} else {
		// Return all stops -- will take a while
		var keys []*datastore.Key
		keys, err = datastore.NewQuery("Stop").Order("__key__").GetAll(c, &stops)

		// Populate IDs
		for i, stop := range stops {
			stop.ID = keys[i].IntID()
		}

	}

	if err != nil {
		http.Error(w, "Get Stops Error: "+err.Error(), 500)
		return
	}

	// Limit response size
	limit := len(stops)
	if r.FormValue("limit") != "" {
		val, limErr := strconv.Atoi(r.FormValue("limit"))
		if limErr == nil && val < limit {
			limit = val
		}
	}

	result := map[string]interface{}{
		"stops": stops[:limit],
	}

	//Print out the pattern information
	data, errJSON := json.Marshal(result)
	if errJSON != nil {
		http.Error(w, errJSON.Error(), 500)
		return
	}

	// Output JSON
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, string(data))
}

func stopsInRadius(c appengine.Context, lat, lng float64, radiusMeters int) ([]*Stop, error) {
	// Check memcache for geohashes
	var geohashToStop map[string]int64
	var sortedGeohash []string

	memcache.Gob.Get(c, "geohashToStop", &geohashToStop)
	memcache.Gob.Get(c, "sortedGeohash", &sortedGeohash)

	if len(geohashToStop) == 0 || len(sortedGeohash) == 0 {
		// Need to repopulate data in memcache

		// Get all stops -- only coordinates
		stops := []Stop{}
		keys, err := datastore.NewQuery("Stop").GetAll(c, &stops)
		if err != nil {
			c.Debugf("Stop QUERY ERROR", stops)
			return nil, err
		}

		// Create geohash map & slice
		geohashToStop = make(map[string]int64)
		sortedGeohash = make([]string, len(stops))

		for i, stop := range stops {
			hash := geohash.EncodeAuto(stop.Lat, stop.Long)
			geohashToStop[hash] = keys[i].IntID()
			sortedGeohash[i] = hash
		}

		//Sort geohashes
		sort.Sort(sort.StringSlice(sortedGeohash))

		// Store in memcache -- hopefully this lasts for a long time
		item1 := &memcache.Item{
			Key:    "geohashToStop",
			Object: geohashToStop,
		}

		item2 := &memcache.Item{
			Key:    "sortedGeohash",
			Object: sortedGeohash,
		}

		memcache.Gob.SetMulti(c, []*memcache.Item{item1, item2})
	}

	// Determine geohash for given position
	currentHash := geohash.EncodeAuto(lat, lng)

	// Determine needed precision
	// REF: http://www.elasticsearch.org/guide/en/elasticsearch/reference/current/search-aggregations-bucket-geohashgrid-aggregation.html
	var precision int
	switch {
	case radiusMeters <= 2000:
		precision = 5 // rough precision
	default:
		precision = 3 // Probably enough to cover everybody -- unproved
	}

	searchFunc := func(i int) bool {
		hash := sortedGeohash[i]
		return strings.HasPrefix(currentHash, hash[:precision])
	}

	// Search for geohash of given position
	startRange := sort.Search(len(sortedGeohash), searchFunc)

	if startRange == len(sortedGeohash) {
		// Not found
		return []*Stop{}, nil
	}

	// Determine end of range
	var endRange int
	for endRange = startRange + 1; endRange < len(sortedGeohash); endRange++ {
		if !searchFunc(endRange) {
			break
		}
	}

	results := sortedGeohash[startRange:endRange]

	// Create array of stopIDS
	desiredStops := make([]*datastore.Key, len(results))
	distances := make([]float64, len(results))

	matchingCount := 0
	for _, stopHash := range results {

		// Decode geohashe
		box, _ := geohash.Decode(stopHash)
		center := box.Center()

		// Determine if this stop matches radius
		stop := geo.NewPoint(center.Lat, center.Lon)
		input := geo.NewPoint(lat, lng)

		dist := stop.GreatCircleDistance(input) * 1000.0 // In meters

		if int(dist) <= radiusMeters {
			k := datastore.NewKey(c, "Stop", "", geohashToStop[stopHash], nil)
			desiredStops[matchingCount] = k
			distances[matchingCount] = dist
			matchingCount++
		}
	}

	// Sort by distance closest to farthest

	var stops []*Stop
	if matchingCount > 0 {
		var err error
		stops, err = getStopsWithKeys(c, desiredStops[:matchingCount])
		if err != nil {
			return nil, err
		}
	}

	// Populate with key and distance
	for i, stop := range stops {
		stop.ID = desiredStops[i].IntID()
		stop.Distance = distances[i]
	}

	// Sort by distance
	sort.Sort(ByDistance{stops})

	// Return stops
	return stops, nil
}

func getStopsWithKeys(c appengine.Context, keys []*datastore.Key) ([]*Stop, error) {
	// Get these stops
	stops := make([]*Stop, len(keys))

	var wg sync.WaitGroup

	for i, key := range keys {
		wg.Add(1)

		go func(idx int, k *datastore.Key) {
			defer wg.Done()

			// Determine Cache Name
			cacheName := "Stop:" + strconv.FormatInt(k.IntID(), 10)

			// Check memcache
			stop := new(Stop)
			if _, memError := memcache.Gob.Get(c, cacheName, stop); memError == memcache.ErrCacheMiss {
				err := datastore.Get(c, k, stop)
				if err != nil {
					c.Debugf("Get Stops Error: ", k)
				}

				// Add to memcache
				item := &memcache.Item{
					Key:    cacheName,
					Object: stop,
				}

				memcache.Gob.Set(c, item) // We do enough work below that this will finish

			}

			// Add to list
			stops[idx] = stop

		}(i, key)

	}

	wg.Wait()

	/*
		for i := 0; i < len(keys); i++ {
			stops[i] = new(Stop)
		}

		err := datastore.GetMulti(c, keys, stops)
		if err != nil {
			c.Debugf("Get Stops Error: ", keys)
			return nil, err
		}
	*/
	return stops, nil
}

func filterArrivalsOnTime(dur time.Duration, arrivals []*Arrival) []*Arrival {
	var i int
	for i = 0; i < len(arrivals); i++ {
		if arrivals[i].Scheduled >= dur {
			break
		}
	}

	return arrivals[i:]
}
