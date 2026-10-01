-- Teams targets must be removed before the constraint can shrink again.
DELETE FROM notification_targets WHERE type = 'teams';
ALTER TABLE notification_targets
    DROP CONSTRAINT notification_targets_type_check;
ALTER TABLE notification_targets
    ADD CONSTRAINT notification_targets_type_check
        CHECK (type IN ('slack', 'email', 'pagerduty', 'webhook'));
