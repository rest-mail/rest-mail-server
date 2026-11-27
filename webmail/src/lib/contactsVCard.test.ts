import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { contactVCard, contactsVCardFile, foldVCardLine, isVCardFile, vcardText, type VCardContact } from './contactsVCard';

const contact: VCardContact = {
  id: 7,
  email: 'ada@example.com',
  name: 'Ada Lovelace',
  trust_level: 'trusted',
  source: 'manual',
  created_at: '2026-11-20T10:11:12.123456Z',
  updated_at: '2026-11-21T08:09:10.654321Z',
};

test('a contact is written as the console writes it, keeping microsecond timestamps', () => {
  assert.equal(contactVCard(contact, 'https://mail.example', 3), [
    'BEGIN:VCARD',
    'VERSION:4.0',
    'UID:https://mail.example/api/v1/accounts/3/contacts/7',
    'FN:Ada Lovelace',
    'EMAIL:ada@example.com',
    'X-RESTMAIL-TRUST-LEVEL:trusted',
    'X-RESTMAIL-SOURCE:manual',
    'X-RESTMAIL-CREATED-AT:2026-11-20T10:11:12.123456Z',
    'REV:20261121T080910Z',
    'X-RESTMAIL-UPDATED-AT:2026-11-21T08:09:10.654321Z',
    'END:VCARD',
    '',
  ].join('\r\n'));
});

test('an unnamed contact is named by its address, and a name equal to the address is marked explicit', () => {
  assert.match(contactVCard({ ...contact, name: '' }, 'https://mail.example', 3), /\r\nFN:ada@example\.com\r\n/);
  assert.match(contactVCard({ ...contact, name: 'ada@example.com' }, 'https://mail.example', 3),
    /\r\nFN;X-RESTMAIL-EXPLICIT-NAME=TRUE:ada@example\.com\r\n/);
});

test('text values are escaped', () => {
  assert.equal(vcardText('Lovelace, Ada; \\ "Countess"\nof Lovelace'), 'Lovelace\\, Ada\\; \\\\ "Countess"\\nof Lovelace');
  assert.match(contactVCard({ ...contact, name: 'Lovelace, Ada' }, 'https://mail.example', 3), /\r\nFN:Lovelace\\, Ada\r\n/);
});

test('long lines fold at 75 octets without splitting a UTF-8 character', () => {
  const line = 'FN:' + 'é'.repeat(60);
  const folded = foldVCardLine(line);
  const physical = folded.slice(0, -2).split('\r\n');
  const encoder = new TextEncoder();
  assert.ok(physical.length > 1);
  for (const [index, piece] of physical.entries()) {
    assert.ok(encoder.encode(piece).length <= 75, `line ${index} is ${encoder.encode(piece).length} octets`);
    if (index > 0) assert.equal(piece[0], ' ');
  }
  assert.equal(physical.map((piece, index) => (index > 0 ? piece.slice(1) : piece)).join(''), line);
});

test('a control character or a contact without an id or address is refused', () => {
  assert.throws(() => foldVCardLine('FN:bell\u0007'), /invalid vCard text/);
  assert.throws(() => contactVCard({ ...contact, id: 0 }, 'https://mail.example', 3), /without an id or valid email/);
  assert.throws(() => contactVCard({ ...contact, email: 'Ada <ada@example.com>' }, 'https://mail.example', 3), /without an id or valid email/);
  assert.throws(() => contactVCard({ ...contact, updated_at: 'yesterday' }, 'https://mail.example', 3), /invalid timestamp/);
});

test('every page is read until the server says none follow', async () => {
  const contacts = Array.from({ length: 5 }, (_, index) => ({ ...contact, id: index + 1, email: `c${index}@example.com` }));
  const asked: [number, number][] = [];
  const file = await contactsVCardFile(async (offset, limit) => {
    asked.push([offset, limit]);
    const data = contacts.slice(offset, offset + 2);
    return { data, pagination: { has_more: offset + data.length < contacts.length } };
  }, 'https://mail.example', 3);
  assert.deepEqual(asked.map(([offset]) => offset), [0, 2, 4]);
  assert.ok(asked.every(([, limit]) => limit === 100));
  assert.equal(file.contacts, 5);
  assert.equal(file.text.match(/BEGIN:VCARD/g)?.length, 5);
  assert.equal(file.bytes, new TextEncoder().encode(file.text).length);
});

test('an empty page that says more follow ends the export with an error', async () => {
  await assert.rejects(
    contactsVCardFile(async () => ({ data: [], pagination: { has_more: true } }), 'https://mail.example', 3),
    /empty contacts page/,
  );
});

test('address-book files are recognised by name or type', () => {
  assert.ok(isVCardFile({ name: 'contacts.vcf', type: '' }));
  assert.ok(isVCardFile({ name: 'export', type: 'text/vcard' }));
  assert.ok(isVCardFile({ name: 'export', type: 'text/x-vcard' }));
  assert.ok(!isVCardFile({ name: 'contacts.csv', type: 'text/csv' }));
});

// testdata/contacts-export.vcf is also what cmd/console's writeVCard must write for these
// contacts (TestWebmailContactsExportMatchesTheConsole), and what the server's vCard import
// must read back field for field (TestWebmailContactsExportImportsEveryField). A change to the
// format on one side fails on the other.
const fixtureContacts: VCardContact[] = [
  { id: 7, email: 'ada@example.com', name: 'Lovelace, Ada', trust_level: 'trusted', source: 'manual',
    created_at: '2026-11-20T10:11:12.123456Z', updated_at: '2026-11-21T08:09:10.654321Z' },
  { id: 8, email: 'bob@example.com', name: 'bob@example.com', trust_level: 'blocked', source: 'sent',
    created_at: '2026-11-20T10:11:12.5Z', updated_at: '2026-11-20T10:11:12.5Z' },
  { id: 9, email: 'cy@example.com', name: '', trust_level: 'auto', source: 'import',
    created_at: '2026-11-20T10:11:12Z', updated_at: '2026-11-20T10:11:12Z' },
  { id: 10, email: 'dee@example.com', name: 'Ünïcødé ' + 'é'.repeat(50), trust_level: 'auto', source: 'manual',
    created_at: '2026-11-20T10:11:12Z', updated_at: '2026-11-20T10:11:12Z' },
];

test('the export is byte for byte the shared fixture the console and the server importer are held to', async () => {
  const file = await contactsVCardFile(async () => ({ data: fixtureContacts, pagination: { has_more: false } }), 'https://mail.example', 3);
  assert.equal(file.text, readFileSync(new URL('./testdata/contacts-export.vcf', import.meta.url), 'utf8'));
  assert.equal(file.contacts, 4);
});
