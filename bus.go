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

	//http.HandleFunc("/addThings", sample)

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
	stopIDToExpectedTime := make(map[int64](map[string]([]time.Duration)))

	// Only make CTS request if asking for within 30 min in the future -- CTS restriction
	if diff := filterTime.Sub(currentTime); diff >= 0 && diff < 30*time.Minute {
		// Request CTS realtime information concurrently

		go func(c appengine.Context, stopNumStrings []string, m map[int64](map[string]([]time.Duration)), finChan chan<- error) {
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

					ctsEstimates := make(map[string]([]time.Duration))
					for _, ctsRoute := range ctsRoutes {
						if len(ctsRoute.Destination) > 0 {
							if ctsRoute.Destination[0].Trip != nil {
								// This stop+route as valid ETA
								estDur := time.Duration(ctsRoute.Destination[0].Trip.ETA) * time.Minute

								// Create new slice or append
								estimateSlice, ok := ctsEstimates[ctsRoute.Number]
								if !ok {
									ctsEstimates[ctsRoute.Number] = []time.Duration{estDur}
								} else {
									ctsEstimates[ctsRoute.Number] = append(estimateSlice, estDur)
								}
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

	// Load all arrival information from datastore for given
	arrivals := make(map[int64]([]*Arrival))
	for _, stopNumString := range sepStops {
		stopNum, _ := strconv.ParseInt(stopNumString, 10, 64)

		// Query for arrival
		parent := datastore.NewKey(c, "Stop", "", stopNum, nil)
		q := datastore.NewQuery("Arrival").Ancestor(parent)
		q = q.Filter("Scheduled >=", durationSinceMidnight)
		q = q.Filter(filterTime.Weekday().String()+" =", true)
		q = q.Order("Scheduled")

		var dest []*Arrival
		_, getError := q.GetAll(c, &dest)
		if getError != nil {
			http.Error(w, "Arrival Error: "+getError.Error(), 500)
			continue //Try next loop -- bad stopNum
		}

		arrivals[stopNum] = dest
	}

	// Wait until cts call finishes
	<-finished

	// Create maps with given information
	output := make(map[string][](map[string]string))
	for stopNum, vals := range arrivals {
		etas := stopIDToExpectedTime[stopNum]

		if len(etas) > len(vals) {
			c.Errorf("Arrival mismatch  at stop %d", stopNum, "Time: ", durationSinceMidnight)
		}

		result := make([](map[string]string), len(vals))

		// Loop over arrival array
		for i, val := range vals {
			// Get route
			var route Route
			datastore.Get(c, val.Route, &route)

			// Use this arrival to determine information
			scheduled := val.Scheduled
			expected := scheduled

			ctsExpected, ok := etas[route.Name]
			if ok {
				// Add eta offset
				expected = durationSinceMidnight + ctsExpected[0]

				// Update stored times
				if len(ctsExpected) == 1 {
					delete(etas, route.Name)
				} else {
					etas[route.Name] = ctsExpected[1:]
				}
			}

			//Convert to times
			midnight := time.Date(filterTime.Year(), filterTime.Month(), filterTime.Day(), 0, 0, 0, 0, loc)
			scheduledTime := midnight.Add(scheduled)
			expectedTime := midnight.Add(expected)

			m := map[string]string{
				"Route":     route.Name,
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
		keys, err := datastore.NewQuery("Stop").Project("Lat", "Long").GetAll(c, &stops)
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

	// Get these stops
	stops := make([]*Stop, matchingCount)
	for i := 0; i < matchingCount; i++ {
		stops[i] = new(Stop)
	}

	if matchingCount > 0 {
		err := datastore.GetMulti(c, desiredStops[:matchingCount], stops)
		if err != nil {
			c.Debugf("Get Stops Error: ", desiredStops)
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

//
// Temporary fix for mising polyline issue
//
func sample(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	var routes []*Route
	keys, _ := datastore.NewQuery("Route").GetAll(c, &routes)

	for i, route := range routes {
		if route.Name == "5" {
			route.Polyline = string([]byte{117, 96, 95, 111, 71, 122, 124, 105, 111, 86, 86,
				99, 66, 116, 64, 90, 118, 65, 108, 64, 98, 69, 100, 66, 91, 110, 66, 87, 126, 65, 67, 80,
				119, 64, 106, 69, 71, 102, 64, 105, 64, 100, 68, 103, 64, 126, 67, 79, 120, 64, 123, 65,
				115, 64, 97, 66, 115, 64, 105, 69, 103, 66, 89, 110, 66, 65, 64, 89, 102, 66, 111, 64, 108,
				69, 109, 64, 122, 68, 111, 64, 118, 68, 111, 64, 126, 68, 109, 64, 122, 68, 111, 64, 122,
				68, 111, 64, 98, 69, 63, 63, 105, 65, 63, 105, 67, 66, 115, 70, 66, 107, 64, 64, 117, 64,
				68, 95, 64, 63, 81, 63, 105, 65, 65, 121, 69, 72, 117, 66, 63, 111, 65, 64, 103, 67, 64,
				123, 64, 64, 113, 66, 64, 117, 64, 63, 117, 64, 64, 111, 66, 66, 103, 69, 66, 125, 65, 64,
				103, 64, 64, 113, 64, 64, 97, 64, 66, 89, 70, 83, 72, 85, 80, 109, 64, 106, 64, 99, 64, 86,
				89, 78, 87, 68, 89, 64, 93, 63, 85, 63, 93, 65, 99, 68, 63, 121, 69, 63, 117, 64, 64, 125, 64,
				64, 97, 68, 66, 115, 68, 63, 125, 64, 63, 103, 64, 63, 97, 74, 63, 105, 64, 63, 119, 71, 68,
				123, 66, 63, 95, 64, 63, 121, 71, 68, 119, 64, 63, 87, 65, 123, 68, 63, 81, 64, 119, 64, 72, 117,
				64, 76, 95, 64, 68, 67, 64, 79, 66, 83, 68, 63, 121, 65, 77, 125, 65, 81, 95, 66, 85, 113, 65, 91,
				105, 65, 111, 64, 117, 66, 95, 64, 109, 65, 89, 97, 65, 75, 95, 64, 73, 87, 91, 103, 65, 83, 99,
				65, 100, 66, 105, 64, 110, 64, 77, 112, 64, 69, 96, 65, 70, 110, 64, 78, 124, 64, 94, 122, 64,
				108, 64, 112, 65, 106, 65, 98, 64, 90, 66, 63, 102, 64, 88, 94, 92, 88, 96, 64, 98, 64, 111, 64,
				82, 91, 82, 88, 90, 76, 78, 66, 66, 64, 118, 65, 63, 122, 66, 65, 112, 65, 67, 92, 69, 74, 71,
				76, 73, 118, 64, 95, 65, 88, 106, 64, 124, 64, 126, 65, 78, 90, 120, 64, 98, 66, 94, 112, 64, 86,
				116, 64, 82, 118, 64, 80, 102, 65, 68, 96, 65, 63, 122, 64, 67, 102, 66, 116, 69, 63, 116, 68,
				63, 100, 66, 63, 102, 64, 63, 106, 67, 63, 96, 68, 67, 110, 65, 67, 98, 64, 63, 120, 69, 63, 112, 64, 63,
				110, 67, 64, 84, 63, 92, 63, 88, 65, 86, 69, 88, 79, 98, 64, 87, 108, 64, 107, 64, 84, 81, 82, 73, 88, 71, 96,
				64, 67, 112, 64, 65, 100, 67, 67, 108, 67, 67, 120, 64, 63, 110, 66, 67, 106, 66, 65, 112, 66, 65, 122, 64, 65,
				102, 67, 65, 100, 69, 65, 104, 65, 67, 110, 67, 69, 104, 65, 64, 112, 64, 63, 116, 64, 69, 106, 64, 65, 114, 70,
				67, 102, 65, 65, 103, 65, 64, 89, 63, 88, 63, 78, 65, 79, 64, 124, 64, 65, 108, 64, 63, 102, 66, 65, 94, 123, 66, 78,
				103, 65, 110, 64, 123, 68, 108, 64, 123, 68, 110, 64, 95, 69, 110, 64, 119, 68, 108, 64, 123, 68, 84, 121, 65, 88, 115,
				66, 88, 103, 66, 90, 113, 66, 116, 64, 115, 69, 118, 64, 115, 69, 80, 101, 65, 92, 97, 67, 64, 69, 66, 77, 78, 123, 64, 106,
				64, 73, 84, 67, 72, 99, 64})

		} else if route.Name == "C1" {
			route.Polyline = string([]byte{111, 97, 95, 111, 71, 98, 125, 105, 111, 86, 64, 63, 84,
				67, 88, 103, 66, 119, 64, 91, 69, 88, 93, 118, 66, 71, 94, 71, 90, 67, 76, 65, 68, 111, 64, 102, 69, 101, 64, 114,
				67, 81, 126, 64, 117, 64, 114, 69, 75, 114, 64, 79, 124, 64, 89, 102, 66, 111, 64, 108, 69, 73, 108, 64, 99, 64, 108,
				67, 111, 64, 118, 68, 75, 116, 64, 99, 64, 104, 67, 109, 64, 122, 68, 67, 84, 107, 64, 100, 68, 85, 126, 65, 89, 98, 66,
				121, 64, 63, 121, 67, 66, 89, 63, 117, 64, 63, 99, 68, 66, 107, 64, 64, 117, 64, 68, 99, 64, 63, 77, 63, 105, 65, 65, 95,
				66, 66, 121, 66, 68, 125, 64, 63, 103, 67, 64, 121, 64, 63, 109, 65, 64, 123, 64, 64, 121, 64, 63, 119, 64, 64, 107, 66,
				64, 81, 63, 125, 65, 66, 117, 66, 64, 113, 65, 64, 101, 67, 66, 71, 63, 105, 64, 64, 97, 64, 66, 89, 70, 83, 72, 85, 80, 109,
				64, 106, 64, 99, 64, 86, 89, 78, 71, 64, 79, 66, 89, 64, 93, 63, 85, 63, 95, 67, 65, 97, 65, 63, 117, 66, 63, 99, 66, 63,
				111, 65, 64, 99, 64, 64, 97, 68, 66, 121, 64, 63, 121, 66, 63, 101, 66, 63, 85, 63, 121, 70, 63, 123, 66, 63, 101, 67, 64,
				113, 67, 66, 111, 65, 63, 107, 64, 63, 113, 68, 64, 103, 67, 66, 119, 64, 63, 103, 65, 65, 107, 67, 63, 81, 64, 119, 64, 72, 117,
				64, 76, 99, 64, 70, 79, 66, 83, 68, 65, 110, 64, 67, 100, 65, 81, 114, 66, 63, 68, 79, 124, 64, 111, 64, 106, 67, 93, 100, 65, 83, 98,
				65, 77, 124, 64, 73, 96, 65, 63, 116, 64, 65, 94, 63, 118, 64, 63, 112, 71, 65, 104, 67, 65, 100, 66, 65, 118, 64, 75, 96, 71, 71, 108,
				66, 73, 122, 66, 73, 116, 66, 69, 100, 66, 63, 96, 67, 63, 104, 64, 66, 104, 64, 66, 106, 65, 72, 118, 65, 70, 96, 65, 66, 106, 64, 70, 116,
				66, 64, 112, 65, 63, 100, 66, 63, 112, 69, 63, 104, 74, 65, 116, 64, 63, 126, 72, 63, 114, 64, 65, 112, 74, 65, 108, 64, 67, 96, 65, 71, 122, 64,
				67, 90, 73, 114, 64, 101, 64, 120, 67, 79, 122, 64, 97, 64, 112, 67, 69, 80, 81, 124, 65, 67, 110, 64, 65, 124, 64, 64, 104, 65, 96, 65, 73, 110,
				64, 79, 66, 63, 96, 64, 95, 64, 94, 99, 64, 84, 97, 64, 78, 87, 116, 64, 101, 66, 90, 109, 64, 114, 64, 121, 65, 76, 83, 84, 77, 86, 71, 88, 64, 104, 64,
				76, 66, 64, 110, 65, 110, 64, 94, 82, 84, 84, 94, 90, 72, 74, 90, 94, 94, 98, 64, 84, 88, 108, 64, 124, 64, 110, 64, 114, 64, 86, 82, 90, 80, 90, 76,
				92, 70, 104, 64, 70, 72, 66, 98, 65, 65, 122, 64, 69, 63, 63, 118, 64, 83, 116, 64, 99, 64, 98, 64, 95, 64, 76, 75, 108, 65, 109, 65, 114, 64, 115, 64,
				124, 68, 123, 68, 72, 73, 92, 93, 104, 67, 107, 67, 64, 65, 124, 65, 95, 66, 108, 65, 109, 65, 100, 64, 101, 64, 104, 67, 109, 67, 118, 65, 113, 65,
				112, 65, 123, 65, 116, 64, 103, 65, 64, 65, 96, 66, 99, 67, 106, 64, 125, 64, 106, 67, 109, 69, 70, 77, 122, 66, 113, 68, 92, 107, 64, 122, 68, 107, 71,
				78, 83, 74, 93, 68, 87, 63, 89, 65, 101, 64, 64, 105, 67, 64, 101, 65, 64, 121, 68, 63, 79, 63, 125, 65, 64, 113, 66, 64, 99, 69, 64, 105, 68, 64, 95, 64,
				63, 99, 68, 64, 115, 66, 64, 113, 64, 64, 111, 70, 66, 113, 68, 63, 103, 69, 64, 115, 69, 63, 105, 69, 64, 109, 66, 64, 103, 67, 63, 99, 64, 66, 107, 68, 64,
				109, 66, 67, 115, 64, 69, 89, 75, 93, 108, 64, 107, 64, 84, 81, 82, 73, 88, 71, 96, 64, 67, 112, 64, 65, 104, 65, 65, 122, 64, 65, 102, 69, 67, 110, 66, 67, 116,
				65, 65, 84, 63, 112, 66, 65, 122, 64, 65, 102, 67, 65, 104, 68, 65, 90, 63, 120, 69, 73, 104, 65, 64, 112, 64, 63, 116, 64, 69, 82, 65, 86, 63, 120, 69, 67,
				88, 63, 104, 66, 65, 106, 65, 65, 92, 63, 102, 64, 95, 68, 70, 99, 64, 110, 64, 123, 68, 108, 64, 123, 68, 110, 64, 95, 69, 110, 64, 119, 68, 108, 64, 123, 68,
				110, 64, 109, 69, 88, 103, 66, 90, 113, 66, 116, 64, 115, 69, 118, 64, 115, 69, 110, 64, 103, 69, 64, 69, 66, 77, 78, 123, 64, 106, 64, 73, 84, 67})
		}

		datastore.Put(c, keys[i], route)
	}
}
