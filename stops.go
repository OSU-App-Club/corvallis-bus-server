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
	"github.com/pierrre/geohash"
)

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
