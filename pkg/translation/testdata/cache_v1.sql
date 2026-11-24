DELETE FROM translations WHERE service_name = 'svc_cache';
INSERT INTO translations (service_name, en, uz, version)
VALUES ('svc_cache', '{"k":"old"}', '{"k":"eski"}', 1);
