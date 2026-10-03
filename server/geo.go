package main

import "math"

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	p := math.Pi / 180
	dla, dlo := (lat2-lat1)*p, (lng2-lng1)*p
	a := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(lat1*p)*math.Cos(lat2*p)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
