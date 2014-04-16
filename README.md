Corvallis Bus API
=============

A API service for the [Corvallis Transit System](www.corvallistransit.com)

[![GoDoc](https://godoc.org/github.com/OSU-App-Club/corvallis-bus-server?status.png)](https://godoc.org/github.com/OSU-App-Club/corvallis-bus-server)

The basic documentation can be found on [GoDoc](http://godoc.org/github.com/OSU-App-Club/corvallis-bus-server)


# Usage

This API service is an HTTP GET based set of web services


##Paths

### /arrivals

  * Default: returns nothing
  * Params:
    1. stops -- comma delimited list of stop numbers (required); Default: ""
    2. date -- date in RFC822Z format; Default: "currentDate"

  * Response:
    1. stops: map stopNumber to array of arrival times in RFC822Z
      * Schedule: time bus is scheduled to arrive
      * Expected: time bus will arrive based on real-time data (often equal to scheduled) 

    * Example

    URL: http://www.corvallis-bus.appspot.com/arrivals?stops=13713


  ```json
      {
        "13713":[
          {
            "Expected":"15 Apr 14 16:57 -0700",
            "Route":"4",
            "Scheduled":"15 Apr 14 16:57 -0700"
          },
          {
            "Expected":"15 Apr 14 17:32 -0700",
            "Route":"CVA",
            "Scheduled":"15 Apr 14 17:32 -0700"
          },
          {
            "Expected":"15 Apr 14 18:57 -0700",
            "Route":"4",
            "Scheduled":"15 Apr 14 18:57 -0700"
          }
        ]
      }
    ```

### /routes

  * Default:  returns all routes without stops
  * Params:
    1. names -- comma delimited list of route numbers (optional); Default: ""
    2. stops -- include stop information ["true" or "false"]; Default: "false"
    3. onlyNames -- only include route names ["true" or "false"]; Default: "false"

  * Example

  URL: http://www.corvallis-bus.appspot.com/routes


  ```json
    {
      "routes":[
        {
          "Name":"1",
          "AdditionalName":"OSU, Witham Hill, Hewlett Packard, and Timberhill Shopping Center",
          "Description":"ROUTE 1 provides hourly service to OSU, Witham Hill, Hewlett Packard \u0026 Timberhill Shopping Center  (Equipped with a wheelchair lift.  A bicycle rack is available on a first-come, first-served basis.)",
          "URL":"http://www.corvallisoregon.gov/index.aspx?page=822",
          "Polyline":"_a_oGh{ioVL{@RHdAb@fAd@p@VpClAQlAa@`CCPWvA_@rBq@lEw@xE}@c@_CcAiEgBIj@QdAYfBo@lEm@zDo@vDKn@c@nCm@zDo@zDMx@a@hC{@~Fy@|FGj@CPk@nEo@nEO\\a@j@i@n@{@dAcA~AyBrCUXo@v@iB`CYf@?l@?\\?jD?T@`E?nE@bE?^FhD?p@?DGpC@xE}@?cC@}D?{@?k@@eABS?gB?sA@eB?{EB_A?{D@uAtBCDKLoDzFKNyC|EGLgBxCc@r@k@|@aBbCw@hAs@x@]`@wApAiClC_A`As@p@_B`BiCjCSTIF}DzD}@|@mAlAq@j@u@b@w@R??{@DcA@s@K]GKGOE[QWSq@s@Yg@EGKMUY_@c@[_@i@g@UU_@ScAi@OGi@MYAWFULMRoAfCu@dBOVU`@WZGFa@^s@NaAHAiA@}@Bo@P}ALw@XkBN{@d@yCHs@JwABaA@m@@eL?_I@_L?wHAqAGuBCk@GaAIwAGuB?i@?aCDeBHuBPiFJaGB}C@iC?qG?w@@uAHaAL}@RcA\\eAn@kCNcAPsBDuB?yAM}AQ_BUqA[iAo@uB_@mAYaAUw@[gAScAUcBIuACyAAcC?qB@wA?qA?uAGeBCq@AuU?m@BqAB_AFoBL}AJoADo@Bs@?gAEmBGy@QmAO_AKs@Iy@CYCa@Eq@Cq@CuB?oAEgECuD?mE?aCBk@H{@Jo@z@cEXy@d@uAPcANmA@m@AcBA}JBo@Ju@X}AV_Aj@_BrD{Iz@mBX_@PIj@Kh@Hj@\\^l@|AdD^f@@BXXZJZFfA@pAGt@Br@Np@TxCnBfFhEp@ZVcAB_@@aHpA??{DIWOMOCM@OLIRA`@@~CA`HC^WbAq@[gFiEk@a@mBmAq@Us@Ou@CqAF@zC?zABbC?bDDfJBjDFlIDtH@~@?~E?dBeGsCo@[_DwAqDaB}EyB{CwAKn@Iz@Cj@?`C?vA?tBBtDDfE?nABtBBp@Dp@B`@BXHx@Jr@N~@PlAFx@DlB?fACr@En@KnAM|AGnBC~@CpA?l@@tB?~QBp@FdB?tA?pAAvA?pB@bCBxAHtATbB~Ag@DAn@Mp@E`AFlBn@lCxBnAt@x@~@d@fAJ`@NvAEvGHnCw@?sEAQ@w@Hu@Lc@FOBSDEtBQrBObAo@jC]dASbAM|@I`A?b@Ap@?v@?pGAhCC|C_A?k@Lk@Zi@f@]v@yAvEg@zAwA~D[r@w@jAs@r@u@h@}@^^dCh@|DTdBL`ARzBBd@NnCHzBBtB@vAj@CXHdA\\`@Nf@T`AZ~@HlAC`B??~H?|HAfBAl@C`AKvAIr@e@xCOz@g@bDQ|ACn@A|@@hA`AIr@O`@_@^c@Ta@NWt@eBZm@r@yALSTMVGX@h@LrAp@^RTTh@f@Z^^b@TXj@|@p@r@VRZPZL\\Fr@JbAAz@Ev@St@c@p@k@Z[p@q@zFyF\\]hCkC~AaBrBsBf@i@`BcBvAqApA{Av@iA`BcCj@}@jCmEFMt@mAbBoCzDkGNStAuBzFAz@AdGAzDAR?dACfBA|D?`EA?_BAyBFwC?q@GiD?_@AcE?oEAaE?U?iE?m@Xg@`AoAf@q@n@w@nCmDbA_BdBuB`@k@N]n@oEn@aFFk@x@}Fz@_Gn@cEn@{Dl@{Dn@_En@wDTwAVcBn@mEXgBZqBt@sEv@sEn@gE@EBMN{@j@ITyA",
          "Color":"00ADEE",
          "Direction":"Clockwise",
          "Start":"2013-07-23T07:00:00Z",
          "End":"2015-07-23T07:00:00Z"
        }
      ]
    }
  ```

### /stops

  * Default:  returns all stops
  * Params:
    1. ids: comma delimited list of stop ids (optional); Default: ""
    2. lat: latitude for search; Default: ""
    3. lng: longitude for search; Default: ""
    4. radius: radius in meters to make search; Default: 500
    5. limit: limit the amount of stops returned; Default: none

  * Response:
    * stops: array of stops objects
        *  Sort order different based on paramaters
          1. location: sorted by distance
          2. ids: sorted by ids


  * Example

  URL: http://www.corvallis-bus.appspot.com/stops?lat=44.57181000&lng=-123.2910000&radius=200&limit=1


  ```json
      {
        "stops":[
          {
            "Name":"NW Harrison Blvd \u0026 NW 36th St",
            "Road":"NW Harrison Blvd",
            "Bearing":181.3342,
            "AdherancePoint":false,
            "Lat":44.57181054,
            "Long":-123.2914071,
            "ID":12483,
            "Distance":32.24711617308209
          }
        ]
      }
  ```
