package corvallisbus

import (
	cts "github.com/cvanderschuere/go-connexionz"
	"encoding/json"
	"fmt"
	"net/http"

	"appengine"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	http.HandleFunc("/platforms", platforms)
	http.HandleFunc("/routes", routes)
	http.HandleFunc("/patterns", patterns)
}

func routes(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	c := cts.New(context, baseURL)

	//Create Platform (Need tag or number)
	p := &cts.Platform{
		Tag: r.FormValue("tag"),
	}

	//Update ETA
	routes, err := c.ETA(p)

	if err != nil {
		http.Error(w,err.Error(),500)
		return
	}

	//Return as json
	data, errJSON := json.Marshal(routes)
	if errJSON != nil {
		http.Error(w,errJSON.Error(),500)
		return
	}

	w.Header().Set("Content-Type","application/json")
	fmt.Fprint(w, string(data))
}

func platforms(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	//Query for all platforms
	c := cts.New(context, baseURL)
	platforms, err := c.Platforms()

	if err != nil {
		http.Error(w,err.Error(),500)
		return
	}

	//Return as json
	data, errJSON := json.Marshal(platforms)
	if errJSON != nil {
		http.Error(w,errJSON.Error(),500)
		return
	}

	w.Header().Set("Content-Type","application/json")
	fmt.Fprint(w, string(data))
}

func patterns(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	c := cts.New(context, baseURL)

	routes,err := c.Patterns()
	if err != nil {
		http.Error(w,err.Error(),500)
		return
	}

	for _,route := range routes{
		if route.Number == r.FormValue("routeNum"){
			//Print out the pattern information
			data,errJSON := json.Marshal(route.Destination[0].Patterns[0])
			if errJSON != nil {
				http.Error(w,errJSON.Error(),500)
				return
			}

			w.Header().Set("Content-Type","application/json")
			fmt.Fprint(w,string(data))
		}
	}


}
