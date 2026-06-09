package feeds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// USGS pulls significant recent earthquakes from the USGS GeoJSON feed (free, no
// key, near-real-time). Best-in-class earthquake geolocation + felt data.
type USGS struct{ url string }

func NewUSGS(url string) *USGS {
	if url == "" {
		// M4.5+ over the last week — global, current, sensible volume.
		url = "https://earthquake.usgs.gov/earthquakes/feed/v1.0/summary/4.5_week.geojson"
	}
	return &USGS{url: url}
}

func (u *USGS) Name() string { return "USGS" }

type usgsResp struct {
	Features []struct {
		ID         string `json:"id"`
		Properties struct {
			Mag     float64 `json:"mag"`
			Place   string  `json:"place"`
			Time    int64   `json:"time"` // ms epoch
			Title   string  `json:"title"`
			Tsunami int     `json:"tsunami"`
		} `json:"properties"`
		Geometry struct {
			Coordinates []float64 `json:"coordinates"` // [lng, lat, depth]
		} `json:"geometry"`
	} `json:"features"`
}

func (u *USGS) Fetch(ctx context.Context, client *http.Client) ([]model.Crisis, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usgs http %d", resp.StatusCode)
	}
	var data usgsResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	out := make([]model.Crisis, 0, len(data.Features))
	for _, f := range data.Features {
		if len(f.Geometry.Coordinates) < 2 || f.ID == "" {
			continue
		}
		lng, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
		started := time.UnixMilli(f.Properties.Time).UTC()
		nature := "earthquake"
		if f.Properties.Tsunami == 1 {
			nature = "tsunami"
		}
		title := f.Properties.Title
		if title == "" {
			title = fmt.Sprintf("M %.1f earthquake", f.Properties.Mag)
		}
		out = append(out, model.Crisis{
			ID:        "usgs-" + f.ID,
			Title:     title,
			Area:      f.Properties.Place,
			Nature:    nature,
			CenterLat: lat,
			CenterLng: lng,
			Source:    "feed:USGS",
			StartedAt: started,
			RadiusKm:  eqRadiusKm(f.Properties.Mag),
			// Feed detections are born 'proposed': a hazard signal is not yet an
			// operation. The first assigned ground report (or an analyst) activates it.
			Status:    "proposed",
		})
	}
	return out, nil
}

// eqRadiusKm is a coarse felt/affected-area radius from magnitude (heuristic;
// production would use USGS ShakeMap MMI contours).
func eqRadiusKm(m float64) float64 {
	switch {
	case m < 4.5:
		return 15
	case m < 5.5:
		return 30
	case m < 6.5:
		return 60
	case m < 7.5:
		return 120
	default:
		return 200
	}
}
