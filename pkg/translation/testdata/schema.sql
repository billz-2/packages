-- Keep in sync with billz_auth_service/migrations/postgres/000164_translations.up.sql
CREATE TABLE IF NOT EXISTS translations (
    id           serial PRIMARY KEY,
    service_name text        NOT NULL UNIQUE,
    ru           jsonb       NOT NULL DEFAULT '{}',
    en           jsonb       NOT NULL DEFAULT '{}',
    uz           jsonb       NOT NULL DEFAULT '{}',
    saved        boolean     NOT NULL DEFAULT true,
    version      bigint      NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION translations_mark_unsaved() RETURNS trigger AS $$
BEGIN
    IF NEW.service_name IS DISTINCT FROM TG_ARGV[0] THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'INSERT' THEN
        NEW.saved := false;
        NEW.version := 1;
    ELSIF NEW.ru IS DISTINCT FROM OLD.ru
        OR NEW.en IS DISTINCT FROM OLD.en
        OR NEW.uz IS DISTINCT FROM OLD.uz THEN
        NEW.saved := false;
        NEW.version := OLD.version + 1;
        NEW.updated_at := now();
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS translations_mark_unsaved_trg ON translations;
CREATE TRIGGER translations_mark_unsaved_trg
    BEFORE INSERT OR UPDATE ON translations
    FOR EACH ROW EXECUTE FUNCTION translations_mark_unsaved('svc_self');
