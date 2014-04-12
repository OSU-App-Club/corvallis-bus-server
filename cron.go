package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"archive/zip"
	"bytes"
	"encoding/csv"
	cts "github.com/cvanderschuere/go-connexionz"
	"github.com/jlaffaye/ftp"
	"io"
	"io/ioutil"
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

	//
	// Download Information
	//

	// Connect to ftp
	ftpCon, connErr := ftp.Connect("ftp.ci.corvallis.or.us:21") // Port 21 in default for FTP
	if connErr != nil {
		c.Errorf("Zip Read Error (FTP Connect): ", connErr)
		return connErr
	}

	// Login (Need to do but doesn't matter values)
	errLogin := ftpCon.Login("anonymous", "anonymous")
	if errLogin != nil {
		c.Errorf("Zip Login Error (FTP Login): ", errLogin)
		return errLogin
	}

	file, err := ftpCon.Retr("/pw/Transportation/GoogleTransitFeed/Google_Transit.zip")
	if err != nil {
		c.Errorf("Zip Read Error (FTP): ", err)
		return err
	}

	bs, errRead := ioutil.ReadAll(file)
	file.Close()
	if errRead != nil {
		c.Errorf("Zip Read Error: ", errRead)
		return errRead
	}

	//
	// Unzip
	//

	reader := bytes.NewReader(bs)

	zipFile, zipError := zip.NewReader(reader, int64(len(bs)))
	if zipError != nil {
		c.Errorf("Zip Inter Error: ", zipError)
		return zipError
	}

	//Convert into map between filename and information
	fileMap := make(map[string]io.ReadCloser)
	for _, f := range zipFile.File {
		r, _ := f.Open()
		defer r.Close()
		fileMap[f.Name] = r
	}

	//
	// Use each file to provide particular information (reference by filename)
	//

	// routes.txt (color, long name, description, url)
	r := csv.NewReader(fileMap["routes.txt"])
	records, _ := r.ReadAll()

	// Read all routes from datastore
	var routes []*Route
	keys, _ := datastore.NewQuery("Route").GetAll(c, &routes)

	// Convert to map -- easier access
	routeMap := make(map[string]*Route)
	keyMap := make(map[string]*datastore.Key)
	for i, route := range routes {
		routeMap[route.Name] = route
		keyMap[route.Name] = keys[i] // Store key for storing later
	}

	// Format of record
	// route_name[0], long_name[3], desc[4], url[6], color[7]
	for _, record := range records[1:] {
		// Get this route
		key, ok := routeNameConversion[record[0]]
		if !ok {
			continue //No matching route in connexionz
		}

		route := routeMap[key] // Route by GT name

		//Update information
		route.AdditionalName = record[3]
		route.Description = record[4]
		route.URL = record[6]
		route.Color = record[7]

		// Update in datastore
		datastore.Put(c, keyMap[route.Name], route)
	}

	// calendar.txt (service_id[0], monday->friday[1:7], startDate[8], endDate[9])
	// map[service_id] Sched

	// trips.txt (route_id[0],service_id[1],trip_id[2])
	// Find mappings between values that are only present in GT information

	// map[service_id] route_id -- can actually be a direct if calendar.txt is done first

	// map[trip_id] route_id -- used in stop_times.txt

	// stop_times.txt (trip_id[0],)

	return nil
}

// Don't forget to call AllocateIDs to prevent overlap with previous keys
// func AllocateIDs(c appengine.Context, kind string, parent *Key, n int) (low, high int64, err error)
