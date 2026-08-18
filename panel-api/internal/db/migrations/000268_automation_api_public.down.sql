-- Reverse 000268: drop the Automation-API-on-443 opt-in flag.
ALTER TABLE server_settings
    DROP COLUMN automation_api_public_enabled;
