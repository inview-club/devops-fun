package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/miekg/dns"
	opensearch "github.com/opensearch-project/opensearch-go"
)

// ISSLocation represents a document for OpenSearch
type ISSLocation struct {
	Name      string    `json:"name"`
	Point     string    `json:"point"`      // geo_point as "lat,lon"
	AltitudeM float64   `json:"altitude_m"` // meters
	Timestamp time.Time `json:"timestamp"`
}

// Convert degrees, minutes, seconds, direction to decimal degrees
func dmsToDecimal(deg, min, sec float64, dir string) float64 {
	dec := deg + min/60.0 + sec/3600.0
	if dir == "S" || dir == "W" {
		dec = -dec
	}
	return dec
}

// Parse LOC string like "47 24 53.500 N 66 12 12.070 W 430520m ..."
func parseLOC(locStr string) (lat, lon, alt float64, err error) {
	re := regexp.MustCompile(`(\d+)\s+(\d+)\s+([\d.]+)\s+([NS])\s+(\d+)\s+(\d+)\s+([\d.]+)\s+([EW])\s+([\d.]+)m`)
	matches := re.FindStringSubmatch(locStr)
	if len(matches) != 10 {
		return 0, 0, 0, fmt.Errorf("LOC string not matched: %s", locStr)
	}

	latDeg, _ := strconv.ParseFloat(matches[1], 64)
	latMin, _ := strconv.ParseFloat(matches[2], 64)
	latSec, _ := strconv.ParseFloat(matches[3], 64)
	latDir := matches[4]

	lonDeg, _ := strconv.ParseFloat(matches[5], 64)
	lonMin, _ := strconv.ParseFloat(matches[6], 64)
	lonSec, _ := strconv.ParseFloat(matches[7], 64)
	lonDir := matches[8]

	alt, _ = strconv.ParseFloat(matches[9], 64)

	lat = dmsToDecimal(latDeg, latMin, latSec, latDir)
	lon = dmsToDecimal(lonDeg, lonMin, lonSec, lonDir)

	return lat, lon, alt, nil
}

func fetchISSLOC(domain string) (lat, lon, alt float64, err error) {
	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeLOC)

	r, _, err := c.Exchange(m, "8.8.8.8:53")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("DNS query failed: %w", err)
	}

	for _, ans := range r.Answer {
		if loc, ok := ans.(*dns.LOC); ok {
			// Convert LOC struct to human-readable string
			locStr := loc.String()
			// Parse string manually
			return parseLOC(locStr)
		}
	}

	return 0, 0, 0, fmt.Errorf("no LOC record found")
}

func main() {
	// 🔹 Create OpenSearch client
	cert, err := tls.LoadX509KeyPair("./elkcert.crt", "./elkcert.key")
	if err != nil {
		log.Fatalf("failed to get certs: %s", err.Error())
	}

	client, err := opensearch.NewClient(opensearch.Config{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				Certificates:       []tls.Certificate{cert},
			},
		},
		Addresses: []string{"https://localhost:9200"},
		Username:  "admin",
		Password:  "admin",
	})

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	domain := "where-is-the-iss.dedyn.io"

	for {
		lat, lon, alt, err := fetchISSLOC(domain)
		if err != nil {
			log.Printf("Failed to fetch ISS LOC: %s", err)
			<-ticker.C
			continue
		}

		point := fmt.Sprintf("%f,%f", lat, lon)
		doc := ISSLocation{
			Name:      domain,
			Point:     point,
			AltitudeM: alt,
			Timestamp: time.Now().UTC(),
		}

		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(doc); err != nil {
			log.Printf("Error encoding document: %s", err)
			<-ticker.C
			continue
		}

		docID := fmt.Sprintf("%d", time.Now().Unix())
		res, err := client.Index(
			"iss",
			&buf,
			client.Index.WithDocumentID(docID),
			client.Index.WithContext(context.Background()),
		)
		if err != nil {
			log.Printf("Failed to index document: %s", err)
		} else {
			log.Printf("Indexed ISS location: lat=%f lon=%f alt=%.2fm", lat, lon, alt)
		}
		if res != nil {
			res.Body.Close()
		}

		<-ticker.C
	}
}
