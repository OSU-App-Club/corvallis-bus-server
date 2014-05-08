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

	"github.com/kellydunn/golang-geo"
)

//
// Global
//

var stopLocations map[int64]*geo.Point // StopID to location point

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
func Stops(c appengine.Context, w http.ResponseWriter, r *http.Request) {
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
		} else if limErr != nil {
			http.Error(w, "Get Stops Error: "+limErr.Error(), 500)
			return
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

	// Check if we need to populate memory
	if stopLocations == nil {
		// Get all stops -- only coordinates
		stops := []Stop{}
		keys, err := datastore.NewQuery("Stop").GetAll(c, &stops)
		if err != nil {
			c.Debugf("Stop QUERY ERROR", stops)
			return nil, err
		}

		stopLocations = make(map[int64]*geo.Point)

		for i, stop := range stops {
			stopLocations[keys[i].IntID()] = geo.NewPoint(stop.Lat, stop.Long)
		}
	}

	// Determine geohash for given position
	currentLoc := geo.NewPoint(lat, lng)

	// Create array of stopIDS
	desiredStops := make([]*datastore.Key, len(stopLocations))
	distances := make([]float64, len(stopLocations))

	// Filter by distance
	matchingCount := 0
	for key, loc := range stopLocations {
		dist := currentLoc.GreatCircleDistance(loc) * 1000.0 // In meters
		if int(dist) <= radiusMeters {
			desiredStops[matchingCount] = datastore.NewKey(c, "Stop", "", key, nil)
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

				wg.Add(1)
				go func() {
					defer wg.Done()
					memcache.Gob.Set(c, item) // We do enough work below that this will finish
				}()
			}

			// Add to list
			stops[idx] = stop

		}(i, key)
	}

	wg.Wait()

	return stops, nil
}
