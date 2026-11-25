#!/bin/sh
set -eu

psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set=ON_ERROR_STOP=1 \
  --set=runtime_password="$MOBILE_USE_DB_ROLE_PASSWORD" <<'SQL'
CREATE ROLE mobileuse LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD :'runtime_password';
ALTER DATABASE mobileuse OWNER TO mobileuse;
ALTER SCHEMA public OWNER TO mobileuse;
SQL
