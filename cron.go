package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	cts "github.com/cvanderschuere/go-connexionz"
	"net/http"
)

// Sets up datastore to fit current structure -- removes all existing
func CTS_InfoInit(w http.ResponseWriter, r *http.Request) {

	c := appengine.NewContext(r)

	clearDatastore(c) //Delete everything

	// Create CTS Client
	client := cts.New(c, baseURL)

	// Download all route patterns -- create blank stops & routes
	addRoutes(client, c)

	// Download platforms and update datastore
	plats, _ := client.Platforms()
	updatePlatforms(plats, c)

	// Create map between platform tag and platform number -- GoogleTransit uses tag
	// tag -> number
	tagToNumber := make(map[int64]int64)
	for _, p := range plats {
		tagToNumber[p.Tag] = p.Number
	}

	// Create map between pattern names in Connexionz and GT (static for now)
	// connexionz -> GT
	nameMap := map[string]string{
		"1":    "R1",
		"2":    "R2",
		"3":    "R3",
		"4":    "R4",
		"5":    "R5",
		"6":    "R6",
		"7":    "R7",
		"8":    "R8",
		"BBN":  "BB_N",
		"BBSE": "BB_SE",
		"BBSW": "BB_SW",
		"C1":   "C1",
		"C2":   "C2",
		"C3":   "C3",
	}

	// Download/parse/input Google Transit
	updateWithGoogleTransit(c, tagToNumber, nameMap)

}

func CTS_ETAUpdate(w http.ResponseWriter, r *http.Request) {

}

//
// Internal functions for CRON
//

//Clears all information in datastore
func clearDatastore(c appengine.Context) error {

	keys, err := datastore.NewQuery("").KeysOnly().GetAll(c, nil)
	if err != nil || len(keys) == 0 {
		return err
	}

	//Delete these keys
	return datastore.DeleteMulti(c, keys)
}

func addRoutes(client *cts.CTS, c appengine.Context) error {

	// Download all patterns
	routes, error := client.Patterns()
	if error != nil {
		return error
	}

	// Add routes
	for _, route := range routes {
		//c.Debugf("Route: ", route)

		// Route dependend on name and direction ( no proper naming convention yet)
		for _, dest := range route.Destination[:1] { // FIXME
			//c.Debugf("Dest: ", dest)

			r := &Route{
				Name:      route.Number,
				Direction: dest.Patterns[0].Direction,
				Polyline:  dest.Patterns[0].Polyline,
			}

			//Loop over stops
			stops := make([]*datastore.Key, len(dest.Patterns[0].Platforms))
			for i, plat := range dest.Patterns[0].Platforms {
				//c.Debugf("Plat: ", plat)

				//Create stop key
				k := datastore.NewKey(c, "Stop", "", plat.Number, nil)

				stop := &Stop{
					Name:           plat.Name,
					AdherancePoint: plat.ScheduleAdheranceTimepoint,
				}

				//Insert into datastore
				datastore.Put(c, k, stop)

				stops[i] = k
			}

			//Store stop keys
			r.Stops = stops

			//Add route datastore
			rk := datastore.NewIncompleteKey(c, "Route", nil)
			_, errRoute := datastore.Put(c, rk, r)
			if errRoute != nil {
				c.Errorf("Route Put Error: ", errRoute)
			}

		}
	}

	return nil
}

// Add individual information from each platform to existing skeleton
func updatePlatforms(platforms []*cts.Platform, c appengine.Context) error {
	for _, plat := range platforms {

		s := &Stop{}
		k := datastore.NewKey(c, "Stop", "", plat.Number, nil)

		err := datastore.Get(c, k, s)
		if err != nil {
			c.Debugf("Plat(2) Error Get: ", err, plat.Number, plat)
			continue
		}

		//Update with new information
		s.Bearing = plat.Bearing
		s.Road = plat.RoadName
		s.Lat = plat.Location.Latitude
		s.Long = plat.Location.Longitude

		_, err = datastore.Put(c, k, s)
		if err != nil {
			c.Debugf("Plat(2) Error Put: ", err)
			continue
		}
	}

	return nil
}

/*
 * Schedule (calendar.txt)
 * Scheduled Arrivals (stop_times.txt)
 * Route color (routes.txt)
 * Service Exceptions (calendar_dates.txt)
 */
func updateWithGoogleTransit(c appengine.Context, tagToNumber map[int64]int64, routeNameConversion map[string]string) error {

	return nil
}

// Don't forget to call AllocateIDs to prevent overlap with previous keys
// func AllocateIDs(c appengine.Context, kind string, parent *Key, n int) (low, high int64, err error)
