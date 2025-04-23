# Changelog

## v0.0.25-RC-0.2 (2025-04-23)

### Fixed
- Fixed tracing initialization issue when upgrading to OpenTelemetry v1.35.0
- Modified URL parsing logic to handle the new URL format requirements in OTel v1.35.0
- Added string utility function to safely parse Jaeger URLs

### Changed
- Updated Jaeger URL handling to support proper host:port format required by newer OpenTelemetry

## v0.0.24 (previous version)

Previous version changes...