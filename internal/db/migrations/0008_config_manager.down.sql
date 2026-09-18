DROP INDEX IF EXISTS idx_cm_secret_reads_at;
DROP INDEX IF EXISTS idx_cm_secret_reads_config;
DROP INDEX IF EXISTS idx_cm_secret_reads_machine;
DROP TABLE IF EXISTS cm_secret_reads;

DROP INDEX IF EXISTS idx_cm_machine_secrets_created_by;
DROP TABLE IF EXISTS cm_machine_secrets;

DROP TABLE IF EXISTS cm_config_values;

DROP INDEX IF EXISTS idx_cm_configs_created_by;
DROP INDEX IF EXISTS idx_cm_configs_address;
DROP TABLE IF EXISTS cm_configs;

DROP TABLE IF EXISTS cm_registrations;

DROP INDEX IF EXISTS idx_cm_machines_approved_by;
DROP INDEX IF EXISTS idx_cm_machines_state;
DROP INDEX IF EXISTS idx_cm_machines_name;
DROP TABLE IF EXISTS cm_machines;
