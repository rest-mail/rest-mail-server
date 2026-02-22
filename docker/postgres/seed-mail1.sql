-- Seed data for mail1.test
-- Password: password123 (bcrypt hash with {BLF-CRYPT} prefix)

INSERT INTO domains (name, server_type, active, default_quota_bytes)
VALUES ('mail1.test', 'traditional', true, 1073741824)
ON CONFLICT DO NOTHING;

INSERT INTO mailboxes (domain_id, local_part, address, password, display_name, quota_bytes, active)
SELECT d.id, 'alice', 'alice@mail1.test',
       '{BLF-CRYPT}$2a$10$Ir1SPAqgXK89HDlubu5qd.VjRkNeaR12dQSRPpTN/blciA63uHTWu',
       'Alice Smith', 1073741824, true
FROM domains d WHERE d.name = 'mail1.test'
ON CONFLICT DO NOTHING;

INSERT INTO mailboxes (domain_id, local_part, address, password, display_name, quota_bytes, active)
SELECT d.id, 'bob', 'bob@mail1.test',
       '{BLF-CRYPT}$2a$10$Ir1SPAqgXK89HDlubu5qd.VjRkNeaR12dQSRPpTN/blciA63uHTWu',
       'Bob Jones', 1073741824, true
FROM domains d WHERE d.name = 'mail1.test'
ON CONFLICT DO NOTHING;

INSERT INTO mailboxes (domain_id, local_part, address, password, display_name, quota_bytes, active)
SELECT d.id, 'postmaster', 'postmaster@mail1.test',
       '{BLF-CRYPT}$2a$10$Ir1SPAqgXK89HDlubu5qd.VjRkNeaR12dQSRPpTN/blciA63uHTWu',
       'Postmaster', 1073741824, true
FROM domains d WHERE d.name = 'mail1.test'
ON CONFLICT DO NOTHING;

INSERT INTO aliases (domain_id, source_address, destination_address, active)
SELECT d.id, 'info@mail1.test', 'alice@mail1.test', true
FROM domains d WHERE d.name = 'mail1.test'
ON CONFLICT DO NOTHING;
