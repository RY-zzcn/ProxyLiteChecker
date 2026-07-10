package main

import (
	"database/sql"
	"strings"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

type ipGeoCacheEntry struct {
	IP         string
	Metadata   checkmeta.Metadata
	Source     string
	FetchedAt  time.Time
	ExpiresAt  time.Time
	LastError  string
	RetryAfter time.Time
}

func (s *store) IPGeoCache(ip string) (ipGeoCacheEntry, bool, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ipGeoCacheEntry{}, false, nil
	}
	var entry ipGeoCacheEntry
	var fetchedAt, expiresAt, retryAfter sql.NullString
	err := s.db.QueryRow(`
SELECT ip, country, country_name, continent_code, ip_type, asn_org, source,
       fetched_at, expires_at, last_error, retry_after
FROM ip_geo_cache
WHERE ip = ?`, ip).Scan(
		&entry.IP,
		&entry.Metadata.Country,
		&entry.Metadata.CountryName,
		&entry.Metadata.ContinentCode,
		&entry.Metadata.IPType,
		&entry.Metadata.ASNOrg,
		&entry.Source,
		&fetchedAt,
		&expiresAt,
		&entry.LastError,
		&retryAfter,
	)
	if err == sql.ErrNoRows {
		return ipGeoCacheEntry{}, false, nil
	}
	if err != nil {
		return ipGeoCacheEntry{}, false, err
	}
	entry.FetchedAt = parseScheduleTime(fetchedAt.String)
	entry.ExpiresAt = parseScheduleTime(expiresAt.String)
	entry.RetryAfter = parseScheduleTime(retryAfter.String)
	entry.Metadata.GeoSource = entry.Source
	entry.Metadata.GeoUpdatedAt = entry.FetchedAt
	return entry, true, nil
}

func (s *store) SaveIPGeoSuccess(ip string, metadata checkmeta.Metadata, fetchedAt time.Time, expiresAt time.Time) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	source := firstNonEmpty(metadata.GeoSource, "endpoint")
	if _, err := tx.Exec(`
INSERT INTO ip_geo_cache (
  ip, country, country_name, continent_code, ip_type, asn_org, source,
  fetched_at, expires_at, last_error, retry_after, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?)
ON CONFLICT(ip) DO UPDATE SET
  country = excluded.country,
  country_name = excluded.country_name,
  continent_code = excluded.continent_code,
  ip_type = excluded.ip_type,
  asn_org = excluded.asn_org,
  source = excluded.source,
  fetched_at = excluded.fetched_at,
  expires_at = excluded.expires_at,
  last_error = '',
  retry_after = NULL,
  updated_at = excluded.updated_at`,
		ip,
		metadata.Country,
		metadata.CountryName,
		metadata.ContinentCode,
		metadata.IPType,
		metadata.ASNOrg,
		source,
		formatBeijingTime(fetchedAt),
		formatBeijingTime(expiresAt),
		formatBeijingTime(fetchedAt),
	); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE proxy_probe_state SET country = COALESCE(NULLIF(?, ''), country), country_name = COALESCE(NULLIF(?, ''), country_name), continent_code = COALESCE(NULLIF(?, ''), continent_code), ip_type = COALESCE(NULLIF(?, ''), ip_type), asn_org = COALESCE(NULLIF(?, ''), asn_org), geo_source = ?, geo_updated_at = ?, updated_at = datetime('now', '+8 hours') WHERE exit_ip = ?`,
		`UPDATE proxies SET country = COALESCE(NULLIF(?, ''), country), country_name = COALESCE(NULLIF(?, ''), country_name), continent_code = COALESCE(NULLIF(?, ''), continent_code), ip_type = COALESCE(NULLIF(?, ''), ip_type), asn_org = COALESCE(NULLIF(?, ''), asn_org), geo_source = ?, geo_updated_at = ?, updated_at = datetime('now', '+8 hours') WHERE exit_ip = ?`,
		`UPDATE proxy_checks SET country = COALESCE(NULLIF(?, ''), country), country_name = COALESCE(NULLIF(?, ''), country_name), continent_code = COALESCE(NULLIF(?, ''), continent_code), ip_type = COALESCE(NULLIF(?, ''), ip_type), asn_org = COALESCE(NULLIF(?, ''), asn_org), geo_source = ?, geo_updated_at = ?, updated_at = datetime('now', '+8 hours') WHERE exit_ip = ?`,
	} {
		if _, err := tx.Exec(statement,
			metadata.Country,
			metadata.CountryName,
			metadata.ContinentCode,
			metadata.IPType,
			metadata.ASNOrg,
			source,
			formatBeijingTime(fetchedAt),
			ip,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	s.InvalidateStats()
	return nil
}

func (s *store) SaveIPGeoFailure(ip string, message string, retryAfter time.Time) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	_, err := s.db.Exec(`
INSERT INTO ip_geo_cache (ip, last_error, retry_after, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(ip) DO UPDATE SET
  last_error = excluded.last_error,
  retry_after = excluded.retry_after,
  updated_at = excluded.updated_at`,
		ip,
		truncateText(message, 500),
		formatBeijingTime(retryAfter),
		formatBeijingTime(time.Now()),
	)
	return err
}

func mergeIPMetadata(base checkmeta.Metadata, next checkmeta.Metadata) checkmeta.Metadata {
	if next.Country != "" {
		base.Country = next.Country
	}
	if next.CountryName != "" {
		base.CountryName = next.CountryName
	}
	if next.ContinentCode != "" {
		base.ContinentCode = next.ContinentCode
	}
	if next.IPType != "" {
		base.IPType = next.IPType
	}
	if next.ASNOrg != "" {
		base.ASNOrg = next.ASNOrg
	}
	if next.GeoSource != "" {
		base.GeoSource = next.GeoSource
	}
	if !next.GeoUpdatedAt.IsZero() {
		base.GeoUpdatedAt = next.GeoUpdatedAt
	}
	return base
}
