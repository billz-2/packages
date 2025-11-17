ifneq ("$(wildcard Makefile.user)", "")
include Makefile.user
endif


bench-all:
	go test -bench=. -benchtime=1s ./test/...

# Бенчмарк конкурентной записи с сетевыми задержками
bench-concurrent:
	go test -bench=BenchmarkStreamToMinio_ConcurrentWrite -benchtime=1s ./test/... -v

# Запуск только тестов на конкурентную запись
test-concurrent:
	go test -run=TestConcurrentWriteOrderPreservation ./test/... -v

# Детальный бенчмарк с выводом использования памяти
bench-memory:
	go test -bench=BenchmarkStreamToMinio_ConcurrentWrite -benchmem -benchtime=2s ./test/... -v

# Запуск всех тестов с принудительным игнорированием кэша и подробным выводом
test-all:
	go test -count=1 -p 1 ./test/... -v

# Очистка кэша тестов
test-clean:
	go clean -testcache

# Базовое тестирование с детальным выводом
test:
	go test -v ./test/...

.DEFAULT_GOAL:=run

.PHONY: test