package corvallisbus

import (
	"appengine"
	"appengine/datastore"
	"archive/zip"
	"bytes"
	"encoding/csv"
	//"github.com/jlaffaye/ftp"
	"appengine/urlfetch"
	"io"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

/*
 * Uses Zip file at with General Transit Feed Specification
 *    https://developers.google.com/transit/gtfs/reference?csw=1
 *
 * Schedule (calendar.txt)
 * Scheduled Arrivals (stop_times.txt)
 * Route color (routes.txt)
 * Service Exceptions (calendar_dates.txt)
 */
func updateWithGoogleTransit(c appengine.Context, tagToNumber map[int64]int64, routeNameConversion map[string]string) error {

	// Download zip file
	zipFile, zipError := downloadInformation(c)
	if zipError != nil {
		return zipError
	}

	//Convert into map between filename and information
	fileMap := make(map[string]io.ReadCloser)
	for _, f := range zipFile.File {
		r, _ := f.Open()
		defer r.Close()
		fileMap[f.Name] = r
	}

	// Create route map -- used througout
	keyMap, routeMap := createRouteMap(c)

	//
	// Use each file to provide particular information (reference by filename)
	//

	r := csv.NewReader(fileMap["routes.txt"])
	updateRoutes(c, r, keyMap, routeMap, routeNameConversion)

	// trips.txt (route_id[0],service_id[1],trip_id[2])
	// Find mappings between values that are only present in GT information

	r = csv.NewReader(fileMap["trips.txt"])
	tripIDMap, tripIDToServiceID := processTrips(r, routeNameConversion)

	// Read all calendar information
	r = csv.NewReader(fileMap["calendar.txt"])
	calendarMap := processCalendars(r)

	// Create mapping between trip_id -> sched_info
	r = csv.NewReader(fileMap["stop_times.txt"])
	scheduleRouteMap := processStopTimes(r)

	// Create map between stop_id -> platform number
	transFile, _ := os.Open("idToNumber.csv")
	r = csv.NewReader(transFile)
	stopIDToNumber := processStopIDToStopNum(r)

	//
	// Combine all information from above into schedule
	//

	// Loop over each sub-route -- sort SchedInfo
	for trip_id, stops := range scheduleRouteMap {

		// Get related Route from datastore
		connexionzRouteName, ok := tripIDMap[trip_id]
		if !ok {
			continue // Not a route that we handle
		}

		route := routeMap[connexionzRouteName]  // Route by Connexionz name
		routeKey := keyMap[connexionzRouteName] // Key for above route

		// Add start & end information on route (might happen multiple times)
		route.Start = calendarMap[tripIDToServiceID[trip_id]].start
		route.End = calendarMap[tripIDToServiceID[trip_id]].end
		datastore.Put(c, routeKey, route)

		// Sort arrivals by stop sequence
		sort.Sort(ByID(stops))

		// Enter initial point --Arrival Objects (parent is stop)
		createArrival(c, stops[0], true, routeKey, route.Name, stopIDToNumber, calendarMap[tripIDToServiceID[trip_id]].days)

		for i := 0; i < len(stops)-1; i++ {
			if stops[i].arrive != 0 {

				//Keep going until we find next known point
				var j int
				for j = i + 1; j < len(stops)-1; j++ {
					if stops[j].arrive != 0 {
						break
					}
				}

				// j is equal to index of next zero
				diff := j - i
				diffSeconds := (stops[j].arrive - stops[i].arrive).Seconds()

				changeSeconds := int(diffSeconds) / diff
				changeDir := time.Duration(time.Duration(changeSeconds) * time.Second)

				// Enter middle points
				for midI := i + 1; midI < j; midI++ {
					stop := stops[midI]

					// Modify time
					stop.arrive = stop.arrive + (changeDir * time.Duration(midI+1))
					createArrival(c, stop, true, routeKey, route.Name, stopIDToNumber, calendarMap[tripIDToServiceID[trip_id]].days)
				}

				i = j - 1 // Move to next chunk -- used next loop

				// Create arrival
				createArrival(c, stops[j], true, routeKey, route.Name, stopIDToNumber, calendarMap[tripIDToServiceID[trip_id]].days)

			} else {
				// Will always have known point at start and end
				c.Errorf("Unexpected behavior in scheduleRoute: %s (%s)", route.Name, stops[i])
			}
		}
	}

	return nil
}

//
// Internal functions
//

func downloadInformation(c appengine.Context) (*zip.Reader, error) {

	/*
		// Connect to ftp
		ftpCon, connErr := ftp.Connect("ftp.ci.corvallis.or.us:21") // Port 21 in default for FTP
		if connErr != nil {
			c.Errorf("Zip Read Error (FTP Connect): ", connErr)
			return nil, connErr
		}

		// Login (Need to do but doesn't matter values)
		errLogin := ftpCon.Login("anonymous", "anonymous")
		if errLogin != nil {
			c.Errorf("Zip Login Error (FTP Login): ", errLogin)
			return nil, errLogin
		}

		file, err := ftpCon.Retr("/pw/Transportation/GoogleTransitFeed/Google_Transit.zip")
		if err != nil {
			c.Errorf("Zip Read Error (FTP): ", err)
			return nil, err
		}
	*/

	client := urlfetch.Client(c)
	resp, _ := client.Get("https://dl.dropboxusercontent.com/u/3107589/Google_Transit.zip")

	bs, errRead := ioutil.ReadAll(resp.Body)
	//file.Close()
	if errRead != nil {
		c.Errorf("Zip Read Error: ", errRead)
		return nil, errRead
	}

	// Unzip
	reader := bytes.NewReader(bs)
	return zip.NewReader(reader, int64(len(bs)))
}

func createRouteMap(c appengine.Context) (map[string]*datastore.Key, map[string]*Route) {
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

	return keyMap, routeMap
}

func updateRoutes(c appengine.Context, r *csv.Reader, keyMap map[string]*datastore.Key, routeMap map[string]*Route, routeNameConversion map[string]string) {
	// routes.txt (color, long name, description, url)
	records, _ := r.ReadAll()

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
}

func processTrips(r *csv.Reader, routeNameConversion map[string]string) (map[string]string, map[string]string) {
	records, _ := r.ReadAll()

	// Create map: trip_id -> connexionz route name
	tripIDMap := make(map[string]string)
	tripIDToServiceID := make(map[string]string)

	for _, record := range records[1:] {
		connexionzRouteName, ok := routeNameConversion[record[0]]
		if ok {
			tripIDMap[record[2]] = connexionzRouteName
			tripIDToServiceID[record[2]] = record[1]
		}
	}

	return tripIDMap, tripIDToServiceID
}

// Used to represent time information from caledars.txt
type TimeInfo struct {
	days  []bool
	start time.Time
	end   time.Time
}

func processCalendars(r *csv.Reader) map[string]TimeInfo {
	calendars, _ := r.ReadAll()
	calendarMap := make(map[string](TimeInfo))
	for _, cal := range calendars {
		days := make([]bool, 7)
		for i, val := range cal[1:8] {
			if val == "1" {
				days[i] = true
			} else {
				days[i] = false
			}
		}

		//
		// Extract start and end dates and add to route
		//
		loc, _ := time.LoadLocation("US/Pacific")
		start, _ := time.ParseInLocation("20060102", cal[8], loc)
		end, _ := time.ParseInLocation("20060102", cal[9], loc)

		calendarMap[cal[0]] = TimeInfo{
			start: start,
			end:   end,
			days:  days,
		}
	}

	return calendarMap
}

// Temp structure for schedule input
type SchedInfo struct {
	arrive time.Duration
	id     int
	name   string
}

type ByID []*SchedInfo

func (a ByID) Len() int           { return len(a) }
func (a ByID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByID) Less(i, j int) bool { return a[i].id < a[j].id }

func processStopTimes(r *csv.Reader) map[string]([]*SchedInfo) {
	records, _ := r.ReadAll()

	scheduleRouteMap := make(map[string]([]*SchedInfo)) // trip_id -> SchedInfo

	// Build map of schedule points
	// trip_id[0],arrival_time[1], departure_time[2], stop_id[3]
	for _, record := range records[1:] {

		identifer, _ := strconv.Atoi(record[4])
		arrivalTime := timeOfDayStringToDuration(record[1])
		//c.Debugf("Time Err: %s", record[1], arrivalTime, timeErr)

		newS := &SchedInfo{
			arrive: arrivalTime,
			id:     identifer,
			name:   record[3],
		}

		schedSlice, ok := scheduleRouteMap[record[0]]
		if !ok {
			// Make new entry for this trip_id
			scheduleRouteMap[record[0]] = []*SchedInfo{newS}
		} else {
			// Append schedInfo to existing slice
			schedSlice = append(schedSlice, newS)
			scheduleRouteMap[record[0]] = schedSlice
		}
	}

	return scheduleRouteMap
}

func timeOfDayStringToDuration(t string) time.Duration {
	if t == "" {
		return 0
	}

	components := strings.SplitN(t, ":", 3)
	hours, err := strconv.Atoi(components[0])
	if err != nil {
		hours, err = strconv.Atoi(string(components[0][1]))
		if err != nil {
			return 0
		}
	}
	minutes, err := strconv.Atoi(components[1])
	if err != nil {
		return 0
	}
	seconds, err := strconv.Atoi(components[2])
	if err != nil {
		return 0
	}

	tTime := time.Duration(time.Duration(seconds)*time.Second + time.Duration(minutes)*time.Minute + time.Duration(hours)*time.Hour)

	return tTime
}

func processStopIDToStopNum(r *csv.Reader) map[string]int64 {
	stopIDToNumber := make(map[string]int64)
	records, _ := r.ReadAll()

	// stop_id[0], stop_number[1]
	for _, record := range records[1:] {
		if record[1] != "" {
			num, _ := strconv.Atoi(record[1])
			stopIDToNumber[record[0]] = int64(num)
		}
	}
	return stopIDToNumber
}

func createArrival(c appengine.Context, stop *SchedInfo, isScheduled bool, routeKey *datastore.Key, routeName string, stopIDToNumber map[string]int64, days []bool) error {

	// Modify for Downtown Transit Center special case
	if stop.name == "MonroeAve_S_5thSt" {
		stop.name = routeName + "_" + stop.name
	}

	stopNum, ok := stopIDToNumber[stop.name]
	if !ok {
		//c.Errorf("Unknown stop: %s", stop.name)
		return nil // Stops are not guarenteeded to be in both datasources
	}

	stopKey := datastore.NewKey(c, "Stop", "", stopNum, nil)
	newArrivalKey := datastore.NewIncompleteKey(c, "Arrival", stopKey)
	newArrival := Arrival{
		Route:       routeKey,
		Scheduled:   stop.arrive,
		IsScheduled: isScheduled,
		Monday:      days[0],
		Tuesday:     days[1],
		Wednesday:   days[2],
		Thursday:    days[3],
		Friday:      days[4],
		Saturday:    days[5],
		Sunday:      days[6],
	}

	datastore.Put(c, newArrivalKey, &newArrival)

	return nil
}
