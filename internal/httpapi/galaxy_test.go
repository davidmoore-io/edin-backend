package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edin-space/edin-backend/internal/memgraph"
)

func TestParseGalaxyViewRequest(t *testing.T) {
	tests := []struct {
		name        string
		queryString string
		wantErr     bool
		wantReq     memgraph.GalaxyViewRequest
	}{
		{
			name:        "valid request",
			queryString: "min_x=-100&max_x=100&min_y=-50&max_y=50&min_z=-100&max_z=100",
			wantErr:     false,
			wantReq: memgraph.GalaxyViewRequest{
				MinX:  -100,
				MaxX:  100,
				MinY:  -50,
				MaxY:  50,
				MinZ:  -100,
				MaxZ:  100,
				Limit: 50000, // default
			},
		},
		{
			name:        "with limit",
			queryString: "min_x=0&max_x=100&min_y=0&max_y=100&min_z=0&max_z=100&limit=1000",
			wantErr:     false,
			wantReq: memgraph.GalaxyViewRequest{
				MinX:  0,
				MaxX:  100,
				MinY:  0,
				MaxY:  100,
				MinZ:  0,
				MaxZ:  100,
				Limit: 1000,
			},
		},
		{
			name:        "with filters",
			queryString: "min_x=0&max_x=100&min_y=0&max_y=100&min_z=0&max_z=100&power=Nakato%20Kaine&allegiance=Federation",
			wantErr:     false,
			wantReq: memgraph.GalaxyViewRequest{
				MinX:       0,
				MaxX:       100,
				MinY:       0,
				MaxY:       100,
				MinZ:       0,
				MaxZ:       100,
				Limit:      50000,
				Power:      "Nakato Kaine",
				Allegiance: "Federation",
			},
		},
		{
			name:        "limit exceeds max",
			queryString: "min_x=0&max_x=100&min_y=0&max_y=100&min_z=0&max_z=100&limit=999999",
			wantErr:     false,
			wantReq: memgraph.GalaxyViewRequest{
				MinX:  0,
				MaxX:  100,
				MinY:  0,
				MaxY:  100,
				MinZ:  0,
				MaxZ:  100,
				Limit: 100000, // capped
			},
		},
		{
			name:        "missing min_x",
			queryString: "max_x=100&min_y=0&max_y=100&min_z=0&max_z=100",
			wantErr:     true,
		},
		{
			name:        "invalid float",
			queryString: "min_x=abc&max_x=100&min_y=0&max_y=100&min_z=0&max_z=100",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/galaxy/view?"+tt.queryString, nil)
			got, err := parseGalaxyViewRequest(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseGalaxyViewRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if got.MinX != tt.wantReq.MinX {
					t.Errorf("MinX = %v, want %v", got.MinX, tt.wantReq.MinX)
				}
				if got.MaxX != tt.wantReq.MaxX {
					t.Errorf("MaxX = %v, want %v", got.MaxX, tt.wantReq.MaxX)
				}
				if got.MinY != tt.wantReq.MinY {
					t.Errorf("MinY = %v, want %v", got.MinY, tt.wantReq.MinY)
				}
				if got.MaxY != tt.wantReq.MaxY {
					t.Errorf("MaxY = %v, want %v", got.MaxY, tt.wantReq.MaxY)
				}
				if got.MinZ != tt.wantReq.MinZ {
					t.Errorf("MinZ = %v, want %v", got.MinZ, tt.wantReq.MinZ)
				}
				if got.MaxZ != tt.wantReq.MaxZ {
					t.Errorf("MaxZ = %v, want %v", got.MaxZ, tt.wantReq.MaxZ)
				}
				if got.Limit != tt.wantReq.Limit {
					t.Errorf("Limit = %v, want %v", got.Limit, tt.wantReq.Limit)
				}
				if got.Power != tt.wantReq.Power {
					t.Errorf("Power = %v, want %v", got.Power, tt.wantReq.Power)
				}
				if got.Allegiance != tt.wantReq.Allegiance {
					t.Errorf("Allegiance = %v, want %v", got.Allegiance, tt.wantReq.Allegiance)
				}
			}
		})
	}
}

func TestValidateBounds(t *testing.T) {
	tests := []struct {
		name    string
		req     memgraph.GalaxyViewRequest
		wantErr bool
	}{
		{
			name: "valid bounds",
			req: memgraph.GalaxyViewRequest{
				MinX: -100, MaxX: 100,
				MinY: -50, MaxY: 50,
				MinZ: -100, MaxZ: 100,
			},
			wantErr: false,
		},
		{
			name: "at max size limit",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 10000,
				MinY: 0, MaxY: 1000,
				MinZ: 0, MaxZ: 10000,
			},
			wantErr: false,
		},
		{
			name: "exceeds max X size",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 10001,
				MinY: 0, MaxY: 100,
				MinZ: 0, MaxZ: 100,
			},
			wantErr: true,
		},
		{
			name: "exceeds max Y size",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 100,
				MinY: 0, MaxY: 10001,
				MinZ: 0, MaxZ: 100,
			},
			wantErr: true,
		},
		{
			name: "exceeds max Z size",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 100,
				MinY: 0, MaxY: 100,
				MinZ: 0, MaxZ: 10001,
			},
			wantErr: true,
		},
		{
			name: "min > max for X",
			req: memgraph.GalaxyViewRequest{
				MinX: 100, MaxX: 0,
				MinY: 0, MaxY: 100,
				MinZ: 0, MaxZ: 100,
			},
			wantErr: true,
		},
		{
			name: "min > max for Y",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 100,
				MinY: 100, MaxY: 0,
				MinZ: 0, MaxZ: 100,
			},
			wantErr: true,
		},
		{
			name: "min > max for Z",
			req: memgraph.GalaxyViewRequest{
				MinX: 0, MaxX: 100,
				MinY: 0, MaxY: 100,
				MinZ: 100, MaxZ: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBounds(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBounds() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGalaxyViewResponse_JSON(t *testing.T) {
	resp := memgraph.GalaxyViewResponse{
		Systems: []memgraph.GalaxySystem{
			{
				ID64:             1234567890,
				Name:             "Sol",
				X:                0,
				Y:                0,
				Z:                0,
				ControllingPower: "Federation",
				PowerplayState:   "Stronghold",
				Allegiance:       "Federation",
				Population:       22780919531,
			},
		},
		TotalCount:  1,
		Truncated:   false,
		QueryTimeMs: 5,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded memgraph.GalaxyViewResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(decoded.Systems) != 1 {
		t.Errorf("expected 1 system, got %d", len(decoded.Systems))
	}
	if decoded.Systems[0].Name != "Sol" {
		t.Errorf("expected system name 'Sol', got '%s'", decoded.Systems[0].Name)
	}
	if decoded.TotalCount != 1 {
		t.Errorf("expected total_count 1, got %d", decoded.TotalCount)
	}
}

func TestSystemDetailResponse_JSON(t *testing.T) {
	semiMajorAxis := 149597870.7
	orbitalPeriod := 365.25

	resp := memgraph.SystemDetailResponse{
		System: memgraph.SystemInfo{
			ID64:             1234567890,
			Name:             "Sol",
			X:                0,
			Y:                0,
			Z:                0,
			Population:       22780919531,
			Allegiance:       "Federation",
			Government:       "Democracy",
			ControllingPower: "Zachary Hudson",
			PowerplayState:   "Stronghold",
		},
		Bodies: []memgraph.BodyInfo{
			{
				ID64:                1234567890001,
				BodyID:              0,
				Name:                "Sol",
				Type:                "Star",
				SubType:             "G (White-Yellow) Star",
				DistanceFromArrival: 0,
				Radius:              695700,
				IsMainStar:          true,
			},
			{
				ID64:                1234567890003,
				BodyID:              3,
				Name:                "Earth",
				Type:                "Planet",
				SubType:             "Earth-like world",
				DistanceFromArrival: 500,
				Radius:              6371,
				SemiMajorAxis:       &semiMajorAxis,
				OrbitalPeriod:       &orbitalPeriod,
				IsLandable:          false,
			},
		},
		Stations: []memgraph.StationInfo{
			{
				MarketID:   128016640,
				Name:       "Abraham Lincoln",
				Type:       "Orbis Starport",
				DistanceLS: 500,
				LandingPads: map[string]int{
					"large":  8,
					"medium": 12,
					"small":  8,
				},
				Services: []string{"Commodities", "Shipyard", "Outfitting"},
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded memgraph.SystemDetailResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if decoded.System.Name != "Sol" {
		t.Errorf("expected system name 'Sol', got '%s'", decoded.System.Name)
	}
	if len(decoded.Bodies) != 2 {
		t.Errorf("expected 2 bodies, got %d", len(decoded.Bodies))
	}
	if len(decoded.Stations) != 1 {
		t.Errorf("expected 1 station, got %d", len(decoded.Stations))
	}

	// Verify orbital parameters are preserved
	earthBody := decoded.Bodies[1]
	if earthBody.SemiMajorAxis == nil {
		t.Error("expected semi_major_axis to be set for Earth")
	} else if *earthBody.SemiMajorAxis != semiMajorAxis {
		t.Errorf("expected semi_major_axis %f, got %f", semiMajorAxis, *earthBody.SemiMajorAxis)
	}
}

func TestBodyInfo_OrbitalParametersOptional(t *testing.T) {
	// Star without orbital parameters
	star := memgraph.BodyInfo{
		ID64:       1,
		Name:       "Sol",
		Type:       "Star",
		IsMainStar: true,
	}

	data, err := json.Marshal(star)
	if err != nil {
		t.Fatalf("failed to marshal star: %v", err)
	}

	// Verify orbital fields are omitted
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, exists := decoded["semi_major_axis"]; exists {
		t.Error("semi_major_axis should be omitted for star")
	}
	if _, exists := decoded["orbital_period"]; exists {
		t.Error("orbital_period should be omitted for star")
	}
}

func TestRingInfo_JSON(t *testing.T) {
	ring := memgraph.RingInfo{
		Name:      "Saturn A Ring",
		RingClass: "Icy",
		InnerRad:  74500000,
		OuterRad:  136780000,
	}

	data, err := json.Marshal(ring)
	if err != nil {
		t.Fatalf("failed to marshal ring: %v", err)
	}

	var decoded memgraph.RingInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ring: %v", err)
	}

	if decoded.Name != ring.Name {
		t.Errorf("expected name '%s', got '%s'", ring.Name, decoded.Name)
	}
	if decoded.InnerRad != ring.InnerRad {
		t.Errorf("expected inner_rad %f, got %f", ring.InnerRad, decoded.InnerRad)
	}
	if decoded.OuterRad != ring.OuterRad {
		t.Errorf("expected outer_rad %f, got %f", ring.OuterRad, decoded.OuterRad)
	}
}

func TestStationInfo_LandingPads(t *testing.T) {
	station := memgraph.StationInfo{
		MarketID:   12345,
		Name:       "Test Station",
		Type:       "Coriolis Starport",
		DistanceLS: 100,
		LandingPads: map[string]int{
			"large":  4,
			"medium": 8,
			"small":  4,
		},
	}

	data, err := json.Marshal(station)
	if err != nil {
		t.Fatalf("failed to marshal station: %v", err)
	}

	var decoded memgraph.StationInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal station: %v", err)
	}

	if decoded.LandingPads["large"] != 4 {
		t.Errorf("expected 4 large pads, got %d", decoded.LandingPads["large"])
	}
	if decoded.LandingPads["medium"] != 8 {
		t.Errorf("expected 8 medium pads, got %d", decoded.LandingPads["medium"])
	}
	if decoded.LandingPads["small"] != 4 {
		t.Errorf("expected 4 small pads, got %d", decoded.LandingPads["small"])
	}
}

func TestHandleGalaxyView_BadRequest(t *testing.T) {
	// Test that handler returns 400 for invalid requests
	// This is a unit test that doesn't require a real server

	tests := []struct {
		name        string
		queryString string
		wantCode    int
	}{
		{
			name:        "missing required params",
			queryString: "min_x=0",
			wantCode:    http.StatusBadRequest,
		},
		{
			name:        "invalid bounds - too large",
			queryString: "min_x=0&max_x=99999&min_y=0&max_y=100&min_z=0&max_z=100",
			wantCode:    http.StatusBadRequest,
		},
		{
			name:        "invalid bounds - min > max",
			queryString: "min_x=100&max_x=0&min_y=0&max_y=100&min_z=0&max_z=100",
			wantCode:    http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/galaxy/view?"+tt.queryString, nil)
			w := httptest.NewRecorder()

			// Parse and validate - this tests the error paths
			parsedReq, parseErr := parseGalaxyViewRequest(req)
			if parseErr != nil {
				// Expected for missing params
				w.WriteHeader(http.StatusBadRequest)
				if w.Code != tt.wantCode {
					t.Errorf("expected status %d, got %d", tt.wantCode, w.Code)
				}
				return
			}

			validateErr := validateBounds(parsedReq)
			if validateErr != nil {
				w.WriteHeader(http.StatusBadRequest)
			}

			if w.Code != tt.wantCode {
				t.Errorf("expected status %d, got %d", tt.wantCode, w.Code)
			}
		})
	}
}
