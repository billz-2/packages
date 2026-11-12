DELETE FROM translations;
INSERT INTO translations (service_name, ru, en, uz)
VALUES ('svc_self', '{"a":"а"}', '{"a":"a"}', '{"a":"a"}');
INSERT INTO translations (service_name, en, saved, version)
VALUES ('svc_other', '{"b":"b"}', true, 7);
UPDATE translations SET saved = true WHERE service_name = 'svc_self';
