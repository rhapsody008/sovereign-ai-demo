package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/imaging"
	"github.com/rhapsody008/sovereign-ai-demo/internal/store"
)

// SeedIfEmpty writes a few pre-masked sample records so the list is never
// empty on stage. Samples are fake; nothing is sent to NAI. Only the primary
// seeds, so two instances starting together do not both write samples.
func (s *Server) SeedIfEmpty(ctx context.Context) {
	if !s.cfg.SeedSamples || s.cfg.ClusterRole != "primary" {
		return
	}
	n, err := s.store.CountRecords(ctx)
	if err != nil || n > 0 {
		return
	}
	base := time.Now().UTC().Add(-50 * time.Minute)
	pb := store.ProcessedBy{Cluster: s.cfg.ClusterName, Role: s.cfg.ClusterRole, Pod: s.cfg.PodName}
	samples := []store.Record{
		{
			InputType:      "text",
			MaskedText:     "Visitor T*** M* L* (S****321A) requests access to Level 5 on Friday.",
			Summary:        "Building access request for one visitor",
			Classification: "Restricted",
			Entities: []store.Entity{
				{Type: "PERSON_NAME", Masked: "T*** M* L*", Source: "llm"},
				{Type: "NATIONAL_ID", Masked: "S****321A", Source: "regex"},
			},
		},
		{
			InputType:      "photo",
			MaskedText:     "R*** O* | Senior Officer | r***@agency.gov.sg | +65 9*** **47",
			Summary:        "Business card of a government officer",
			Classification: "Confidential",
			HasPhoto:       true,
			Entities: []store.Entity{
				{Type: "PERSON_NAME", Masked: "R*** O*", Source: "llm"},
				{Type: "EMAIL", Masked: "r***@agency.gov.sg", Source: "llm"},
				{Type: "PHONE", Masked: "+65 9*** **47", Source: "regex"},
			},
		},
		{
			InputType:      "text",
			MaskedText:     "Please call P*** K* at 8*** **06 about the procurement schedule.",
			Summary:        "Callback request about a procurement schedule",
			Classification: "Confidential",
			Entities: []store.Entity{
				{Type: "PERSON_NAME", Masked: "P*** K*", Source: "llm"},
				{Type: "PHONE", Masked: "8*** **06", Source: "regex"},
			},
		},
	}
	for i, r := range samples {
		t := base.Add(time.Duration(i) * 15 * time.Minute)
		r.ID, r.CreatedAt, r.ProcessedBy = store.NewID(t), t, pb
		r.Model, r.NAILatencyMs = "sample", int64(600+i*90)
		if r.HasPhoto {
			if thumb, err := imaging.BlurThumb(sampleCard()); err == nil {
				_ = s.store.PutObject(ctx, "thumbs/"+r.ID+".jpg", thumb, "image/jpeg")
			}
		}
		if err := s.store.PutRecord(ctx, r); err != nil {
			s.log.Warn("seed sample record", "err", err)
			return
		}
	}
	s.log.Info("seeded sample records", "count", len(samples))
}

// sampleCard draws a crude business-card image for the seeded photo record.
func sampleCard() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	fill := func(x0, y0, x1, y1 int, c color.Color) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.Set(x, y, c)
			}
		}
	}
	fill(0, 0, 320, 200, color.RGBA{245, 243, 236, 255})
	fill(0, 0, 320, 36, color.RGBA{31, 95, 168, 255})
	fill(20, 60, 200, 80, color.RGBA{40, 40, 40, 255})
	fill(20, 95, 150, 105, color.RGBA{110, 110, 110, 255})
	fill(20, 130, 230, 140, color.RGBA{90, 90, 90, 255})
	fill(20, 150, 180, 160, color.RGBA{90, 90, 90, 255})
	fill(240, 60, 300, 130, color.RGBA{217, 130, 43, 255})
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}
