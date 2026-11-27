package db

import "gorm.io/gorm"

// The worker releases its owner explicitly in the same transaction as a
// terminal outcome. This also covers administrative bounces and deletes
// (including bulk operations) that do not run through the worker.
func migrateQueueSources(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION release_outbound_queue_source()
			RETURNS trigger LANGUAGE plpgsql AS $$
			DECLARE source text;
			BEGIN
				IF TG_OP = 'DELETE' THEN
					source := OLD.source_ref;
				ELSIF NEW.status IN ('delivered', 'bounced', 'expired') THEN
					source := NEW.source_ref;
					NEW.source_ref := NULL;
				END IF;
				IF source IS NOT NULL THEN
					UPDATE message_sources
					SET reference_count = reference_count - 1,
						reap_at = CASE WHEN reference_count = 1 THEN clock_timestamp() ELSE NULL END
					WHERE ref = source AND state = 'published' AND reference_count > 0;
					IF NOT FOUND THEN
						RAISE EXCEPTION 'outbound queue source has no committed reference';
					END IF;
				END IF;
				IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
				RETURN NEW;
			END;
			$$`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE OR REPLACE TRIGGER outbound_queue_source_terminal
			BEFORE UPDATE OF status OR DELETE ON outbound_queue
			FOR EACH ROW EXECUTE FUNCTION release_outbound_queue_source()`).Error
	})
}
