-- Microsoft Teams notification targets (plans/monitoring-prod.md).
-- The handler and the UI gained the "teams" type in #287; the CHECK
-- constraint from 000015 still listed the original four, so creating a
-- Teams target failed with a 500 on the first prod attempt.
ALTER TABLE notification_targets
    DROP CONSTRAINT notification_targets_type_check;
ALTER TABLE notification_targets
    ADD CONSTRAINT notification_targets_type_check
        CHECK (type IN ('slack', 'teams', 'email', 'pagerduty', 'webhook'));
