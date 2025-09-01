// Package internal provides services for media organization, such as extracting metadata from files.
package internal

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/barasher/go-exiftool"
	"github.com/sirupsen/logrus"
)

// ExifToolService wraps the go-exiftool library to provide a thread-safe service for extracting dates from media files.
type ExifToolService struct {
	et *exiftool.Exiftool
	mu sync.Mutex
}

// NewExifToolService creates and initializes a new ExifToolService.
// It starts the underlying exiftool process.
func NewExifToolService() (*ExifToolService, error) {
	et, err := exiftool.NewExiftool()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize exiftool: %w", err)
	}
	return &ExifToolService{et: et}, nil
}

// ExtractDate extracts the date from a media file using exiftool.
// It checks for common date tags ("DateTimeOriginal", "CreateDate", "DateCreated")
// and optionally "FileModifyDate".
// The first valid date found is returned.
func (s *ExifToolService) ExtractDate(path string, debug bool, useFileModifyDate bool) (time.Time, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fileInfos := s.et.ExtractMetadata(path)
	if len(fileInfos) == 0 {
		logrus.Warnf("[EXIF] No metadata extracted for %s", path)
		return time.Time{}, "", nil
	}
	fi := fileInfos[0]

	// Log all metadata as JSON if debug mode is enabled
	if debug {
		metaJSON, err := json.MarshalIndent(fi.Fields, "", "  ")
		if err != nil {
			logrus.Warnf("[EXIF] Failed to marshal metadata for %s: %v", path, err)
		} else {
			logrus.Debugf("Metadata for %s:\n%s", path, string(metaJSON))
		}
	}

	// Define the list of tags to check for a date
	tags := []string{"DateTimeOriginal", "CreateDate", "DateCreated"}
	if useFileModifyDate {
		tags = append(tags, "FileModifyDate")
	}

	// Iterate through the tags and try to parse the date
	for _, tag := range tags {
		if val, found := fi.Fields[tag]; found {
			if dateStr, ok := val.(string); ok {
				if t, err := ParseExifDate(dateStr); err == nil {
					return t, tag, nil
				} else {
					logrus.Warnf("[EXIF] Error parsing date '%s' for tag '%s' in file %s: %v", dateStr, tag, path, err)
				}
			}
		}
	}

	logrus.Infof("[EXIF] No valid date found in metadata for %s", path)
	return time.Time{}, "", nil
}

// ParseExifDate parses a date string from EXIF metadata.
// It supports multiple common date formats.
func ParseExifDate(dateStr string) (time.Time, error) {
	// List of supported date formats
	layouts := []string{
		"2006:01:02 15:04:05-07:00", // With timezone
		"2006:01:02 15:04:05",       // Without timezone
		"2006:01:02",                // Date only
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, dateStr); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized date format: %s", dateStr)
}

// Close terminates the underlying exiftool process.
func (s *ExifToolService) Close() {
	s.et.Close()
}
