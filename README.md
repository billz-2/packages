# billz_platform

package for common functions for all microservices

- how to add new package version:

  - push changes into master
  - git tag -a v0.0.28 -m "v0.0.28: Change wait strategy for Elastic"
  - git push origin v0.0.28
  - wait some time (up to 30 minutes) for proxy to update
  - to update package in microservice execute `go get -u github.com/billz-2/packages`

- how to execute tests
  - docker compose -f ./docker-compose-test.yml up -d
  - go test ./...
