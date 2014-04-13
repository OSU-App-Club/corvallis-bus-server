package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	cts "github.com/cvanderschuere/go-connexionz"
)

func addRoutes(client *cts.CTS, c appengine.Context) error {

	// Download all patterns
	routes, error := client.Patterns()
	if error != nil {
		return error
	}

	// Add routes
	for _, route := range routes {
		//c.Debugf("Route: ", route)

		var dest *cts.Destination
		// Choose the longest destination
		for _, testTest := range route.Destination {
			if dest == nil {
				dest = testTest
			} else if len(dest.Patterns[0].Platforms) < len(testTest.Patterns[0].Platforms) {
				// Determine if this one is longer
				dest = testTest
			}
		}

		// Route dependend on name and direction ( no proper naming convention yet)
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
