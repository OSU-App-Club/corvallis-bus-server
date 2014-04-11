package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	cts "github.com/cvanderschuere/go-connexionz"
)

// Sets up datastore to fit current structure -- removes all existing
func CTS_InfoInit(w http.ResponseWriter, r *http.Request) {

	c := appengine.NewContext(r)

	clearDatastore(c) //Delete everything

	// Create CTS Client

	// Download all route patterns -- create blank stops & routes

	// Create map between platform tag and platform number -- GoogleTransit uses tag

	// Create map between pattern names in Connexionz and GT (static for now)

	// Concurrently input platform info and download/parse/input Google Transit

	// Wait for top two to finish

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

func addRoutes(client cts.CTS, c appengine.Context) error {

	// Download all patterns

	// Add patters to datastore

}

// Add individual information from each platform to existing skeleton
func updatePlatforms(platforms []*cts.Platform) error {

}

/*
 * Schedule (calendar.txt)
 * Scheduled Arrivals (stop_times.txt)
 * Route color (routes.txt)
 * Service Exceptions (calendar_dates.txt)
 */
func updateWithGoogleTransit(c appengine.Context, tagToNumber map[int8]int8, routeNameConversion map[string]string) error {

}

// Don't forget to call AllocateIDs to prevent overlap with previous keys
// func AllocateIDs(c appengine.Context, kind string, parent *Key, n int) (low, high int64, err error)
