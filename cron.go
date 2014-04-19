package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"appengine/runtime"
	"net/http"

	cts "github.com/cvanderschuere/go-connexionz"
)

func init() {
	http.HandleFunc("/cron/init", CreateDatabase)
	http.HandleFunc("/_ah/start", start)
}

func start(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	c.Infof("Started Instance")
}

// Sets up datastore to fit current structure -- removes all existing
func CreateDatabase(w http.ResponseWriter, r *http.Request) {

	context := appengine.NewContext(r)

	// Allows for unlimited time limit
	runtime.RunInBackground(context, func(c appengine.Context) {

		//clearDatastore(c) //Delete everything

		// Create CTS Client
		client := cts.New(c, baseURL)

		// Download all route patterns -- create blank stops & routes
		addRoutes(client, c)

		// Download platforms and update datastore
		plats, _ := client.Platforms()
		updatePlatforms(plats, c)

		// Create map between platform tag and platform number -- GoogleTransit uses tag
		// tag -> number (GT -> connexionz)
		tagToNumber := make(map[int64]int64)
		for _, p := range plats {
			tagToNumber[p.Tag] = p.Number
		}

		// Create map between pattern names in Connexionz and GT (static for now)
		// GT -> connexionz
		nameMap := map[string]string{
			"R1":    "1",
			"R2":    "2",
			"R3":    "3",
			"R4":    "4",
			"R5":    "5",
			"R6":    "6",
			"R7":    "7",
			"R8":    "8",
			"BB_N":  "BBN",
			"BB_SE": "BBSE",
			"BB_SW": "BBSW",
			"C1":    "C1",
			"C2":    "C2",
			"C3":    "C3",
			"CVA":   "CVA",
		}

		// Download/parse/input Google Transit
		updateWithGoogleTransit(c, tagToNumber, nameMap)
	})
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

// Don't forget to call AllocateIDs to prevent overlap with previous keys
// func AllocateIDs(c appengine.Context, kind string, parent *Key, n int) (low, high int64, err error)
