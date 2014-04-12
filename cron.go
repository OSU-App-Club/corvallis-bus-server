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
	"time"
	"strconv"
	"sort"
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

		route := routeMap[key] // Route by Connexionz name

		//Update information
		route.AdditionalName = record[3]
		route.Description = record[4]
		route.URL = record[6]
		route.Color = record[7]

		// Update in datastore
		datastore.Put(c, keyMap[route.Name], route)
	}

	// trips.txt (route_id[0],service_id[1],trip_id[2])
	// Find mappings between values that are only present in GT information

	r = csv.NewReader(fileMap["trips.txt"])
	records, _ = r.ReadAll()

	// Create map: trip_id -> connexionz route name
	tripIDMap := make(map[string]string)
	for _, record := range records[1:] {
		connexionzRouteName, ok := routeNameConversion[record[0]]
		if ok{
				tripIDMap[record[2]] = connexionzRouteName
		}
	}

	// Loop through stop_times.txt and add to appropriate stop
	r = csv.NewReader(fileMap["stop_times.txt"])
	records, _ = r.ReadAll()

	scheduleRouteMap := make(map[string]([]*SchedInfo)) // trip_id -> SchedInfo

	// Build map of schedule points
	for _, record := range records[1:] {

		identifer,_ := strconv.Atoi(record[4])
		arrivalTime,_ := time.Parse("15:04:05",record[1])

		newS := &SchedInfo{
			arrive:arrivalTime,
			id: identifer,
		}

		schedSlice,ok := scheduleRouteMap[record[0]]
		if !ok{
			// Make new entry for this trip_id
			scheduleRouteMap[record[0]] = []*SchedInfo{newS}
		}else{
				// Append schedInfo to existing slice
				schedSlice = append(schedSlice,newS)
				scheduleRouteMap[record[0]] = schedSlice
		}
	}

	// Loop over each sub-route -- sort SchedInfo
	for trip_id,stops := range scheduleRouteMap{

		// Get related Route from datastore
		connexionzRouteName,ok := tripIDMap[trip_id]
		if !ok{
			continue // Not a route that we handle
		}

		route := routeMap[connexionzRouteName] // Route by Connexionz name
		routeKey := keyMap[connexionzRouteName] // Key for above route

		// Sort arrivals by stop sequence
		sort.Sort(ByID(stops))

		// Enter initial point --Arrival Objects
		newArrivalKey := datastore.NewIncompleteKey(c,"Arrival",route.Stops[0])
		newArrival := Arrival{
			Route: routeKey,
			Scheduled: stops[0].arrive,
			IsScheduled: true,
		}

		datastore.Put(c,newArrivalKey,&newArrival)

		for i := 0; i<len(stops); i++{
			if !stops[i].arrive.IsZero(){

				if i == len(stops) - 1 {
					break
				}
				
				//Keep going until we find next known point
				var j int
				for j = i+1;j<len(stops);j++{
					if !stops[j].arrive.IsZero(){
						break
					}
				}

				c.Debugf("J: %d  Length of stops: %d\n", j, len(stops))

				// j is equal to index of next zero
				diff := j-i
				diffSeconds := stops[j].arrive.Sub(stops[i].arrive).Seconds()

				changeSeconds := int(diffSeconds)/diff
				changeDir := time.Duration(time.Duration(changeSeconds) * time.Second)

				// Enter middle points
				for midI,stop := range stops[i+1:j]{
					newArrivalKey = datastore.NewIncompleteKey(c,"Arrival",route.Stops[midI+(i+1)])
					newArrival = Arrival{
						Route: routeKey,
						Scheduled: stop.arrive.Add(changeDir * time.Duration(midI+1)),
						IsScheduled: false,
					}

					datastore.Put(c,newArrivalKey,&newArrival)
				}

				// Enter last point (known point)
				newArrivalKey = datastore.NewIncompleteKey(c,"Arrival",route.Stops[j])
				newArrival = Arrival{
					Route: routeKey,
					Scheduled: stops[j].arrive,
					IsScheduled: true,
				}

				datastore.Put(c,newArrivalKey,&newArrival)

				i = j - 1 // Skip to next chunk

			}else{
				// Will always have known point at start and end
				c.Errorf("Unexpected behavior in scheduleRoute")
			}
		}

	}

	// calendar.txt (service_id[0], monday->friday[1:7], startDate[8], endDate[9])
	// map[service_id] Sched

	// map[service_id] route_id -- can actually be a direct if calendar.txt is done first

	// map[trip_id] route_id -- used in stop_times.txt

	// stop_times.txt (trip_id[0],)

	return nil
}

// Temp structure for schedule input
type SchedInfo struct{
	arrive time.Time
	id	int
}

type ByID []*SchedInfo

func (a ByID) Len() int           { return len(a) }
func (a ByID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByID) Less(i, j int) bool { return a[i].id < a[j].id }

// Don't forget to call AllocateIDs to prevent overlap with previous keys
// func AllocateIDs(c appengine.Context, kind string, parent *Key, n int) (low, high int64, err error)
