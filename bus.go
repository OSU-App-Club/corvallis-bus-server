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

	// Development help
	http.HandleFunc("/platforms", platforms)
	http.HandleFunc("/routes_list", routes_list)
	http.HandleFunc("/cron/init", CreateDatabase)
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
	stopIDToExpectedTime := make(map[int64]([]time.Duration))

	// Only make CTS request if asking for within 30 min in the future -- CTS restriction
	if diff := filterTime.Sub(currentTime); diff >= 0 && diff < 30*time.Minute {
		// Request CTS realtime information concurrently

		go func(c appengine.Context, stopNumStrings []string, m map[int64]([]time.Duration), finChan chan<- error) {
			client := cts.New(c, baseURL)

			for _, stopNumString := range stopNumStrings {
				stopNum, _ := strconv.ParseInt(stopNumString, 10, 64)

				plat := &cts.Platform{Number: stopNum}

				// Make CTS call
				client.ETA(plat)

			}

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

		result := make([](map[string]string), len(vals))

		// Loop over arrival array
		for i, val := range vals {
			// Use this arrival to determine information
			scheduled := val.Scheduled
			expected := scheduled

			if i < len(etas) {
				// Add eta offset
				expected = expected + etas[i]
			}

			//Convert to times
			midnight := time.Date(filterTime.Year(), filterTime.Month(), filterTime.Day(), 0, 0, 0, 0, loc)
			scheduledTime := midnight.Add(scheduled)
			expectedTime := midnight.Add(expected)

			// Get route
			var route Route
			datastore.Get(c, val.Route, &route)

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
		q = q.Project("Name", "Start", "End")
		cacheName += "OnlyName"
	}

	var routes []*Route

	// Try to get from memcache
	if _, memError := memcache.Gob.Get(c, cacheName, &routes); memError == memcache.ErrCacheMiss {
		// Load from datastore

		_, err := q.GetAll(c, &routes)

		if err != nil {
			http.Error(w, "Get Routes Error: "+err.Error(), 500)
			return
		}

		// Save in memcache
		item := &memcache.Item{
			Key:    cacheName,
			Object: routes,
		}

		go memcache.Gob.Set(c, item)

	} else if memError != nil {
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
		chans := make([](chan []*Stop), len(routes))

		for i, route := range routes {
			chans[i] = make(chan []*Stop, 1)

			go func(chan []*Stop) {
				var path []*Stop
				datastore.GetMulti(c, route.Stops, path)
				chans[i] <- path
			}(chans[i])
		}

		// Load all from channels
		for i, resultChan := range chans {
			routes[i].Path = <-resultChan
			close(resultChan)
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

func routes_list(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	//c := cts.New(context, baseURL)

	w.Header().Set("Content-Type", "application/json")

	// Read all routes from datastore
	var routes []*Route
	datastore.NewQuery("Route").GetAll(context, &routes)

	for _, route := range routes {
		fmt.Fprint(w, "Route: ", route.Name, "\n", "len: ", len(route.Stops), "\n")

		for _, stopkey := range route.Stops {
			var stop Stop
			datastore.Get(context, stopkey, &stop)

			fmt.Fprint(w, "\t", stop.Name, "\t\t\t", stopkey.IntID(), "\n")
		}
		fmt.Fprint(w, "\n\n\n")
	}

	//Return as json
	//data, errJSON := json.Marshal(routes)
	//if errJSON != nil {
	//	http.Error(w, errJSON.Error(), 500)
	//	return
	//}
}

func platforms(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	//Query for all platforms
	c := cts.New(context, baseURL)
	platforms, err := c.Platforms()

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	for _, plat := range platforms {
		fmt.Fprintf(w, "Name: ", plat.Name, "Number: ", plat.Number, "\n")
	}

	/*
		//Return as json
		data, errJSON := json.Marshal(platforms)
		if errJSON != nil {
			http.Error(w, errJSON.Error(), 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(data))

	*/
}

func patterns(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	c := cts.New(context, baseURL)

	routes, err := c.Patterns()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	ps := make(map[string]*cts.Pattern)

	for _, route := range routes {
		if route.Number == r.FormValue("routeNum") || r.FormValue("routeNum") == "" {
			ps[route.Number] = route.Destination[0].Patterns[0]
		}
	}

	//Print out the pattern information
	data, errJSON := json.Marshal(ps)
	if errJSON != nil {
		http.Error(w, errJSON.Error(), 500)
		return
	}
	fmt.Fprint(w, string(data))
}
