package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

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
func Routes(c appengine.Context, w http.ResponseWriter, r *http.Request) {
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
