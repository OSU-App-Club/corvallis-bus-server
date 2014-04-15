package corvallisbus

import (
	"encoding/json"
	"fmt"
	cts "github.com/cvanderschuere/go-connexionz"
	"net/http"

	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"sort"
	"strings"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	// New API
	http.HandleFunc("/routes", Routes)
	http.HandleFunc("/arrivals", Arrivals)

	// Development help
	http.HandleFunc("/platforms", platforms)
	http.HandleFunc("/routes_list", routes_list)
	http.HandleFunc("/cron/init", CreateDatabase)
}

// Endpoint to allow access to arrival information
// Accessed through specified stop IDs [limited to 10ish]
func Arrivals(w http.ResponseWriter, r *http.Request) {

}

/*
	/routes (endpoint to access route information)

	Default: returns all routes without detailed stops
	Paramaters:
		names: comma delimited list of route numbers (optional); Default: ""
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

// Endpoint to allow access to stop information
// Accessed through specified stop IDs or by a radius from a given location
func Stops(w http.ResponseWriter, r *http.Request) {

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
