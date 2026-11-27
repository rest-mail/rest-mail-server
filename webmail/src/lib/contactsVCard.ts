// Contacts as an address-book file (vCard 4.0, RFC 6350), written the way the
// console's `export-data contacts` writes them, so a file saved here imports
// back through importContacts with every stored field: name, address, trust
// level, source and both timestamps.

export interface VCardContact {
  id: number;
  email: string;
  name: string;
  trust_level: string;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface ContactsPage {
  data: VCardContact[];
  pagination?: { has_more: boolean } | null;
}

/** The most contacts one page asks for; the API's own list limit. */
export const CONTACTS_PAGE_SIZE = 100;

const encoder = new TextEncoder();

/** vcardText escapes a text value (RFC 6350 section 3.4). */
export function vcardText(value: string): string {
  return value
    .replace(/\r\n?/g, '\n')
    .replace(/\\/g, '\\\\')
    .replace(/;/g, '\\;')
    .replace(/,/g, '\\,')
    .replace(/\n/g, '\\n');
}

/**
 * foldVCardLine ends a content line with CRLF, folding it so no physical line
 * is longer than 75 octets and no UTF-8 character is split (RFC 6350 section 3.2).
 */
export function foldVCardLine(line: string): string {
  // A control character other than tab cannot be written as vCard text.
  if ([...line].some(character => {
    const code = character.codePointAt(0) ?? 0;
    return (code < 0x20 && code !== 0x09) || code === 0x7f;
  })) {
    throw new Error('the server returned invalid vCard text');
  }
  const out: string[] = [];
  let width = 75;
  let piece = '';
  let pieceBytes = 0;
  for (const character of line) {
    const bytes = encoder.encode(character).length;
    if (pieceBytes + bytes > width) {
      out.push(piece);
      piece = '';
      pieceBytes = 0;
      width = 74;
    }
    piece += character;
    pieceBytes += bytes;
  }
  out.push(piece);
  return out.join('\r\n ') + '\r\n';
}

/** A plain address as the server stores it: one @, no spaces, no angle brackets. */
function isStoredAddress(email: string): boolean {
  return /^[^\s<>@]+@[^\s<>@]+$/.test(email);
}

/** basicUTC is a timestamp as REV writes it: 20261127T064938Z. */
function basicUTC(timestamp: string): string {
  const instant = new Date(timestamp);
  if (Number.isNaN(instant.getTime())) throw new Error('the server returned an invalid timestamp');
  return instant.toISOString().replace(/[-:]/g, '').replace(/\.\d+/, '');
}

/** contactVCard is one contact as one card. `base` is the server's origin, for the card's UID. */
export function contactVCard(contact: VCardContact, base: string, accountId: number): string {
  if (!contact.id || !isStoredAddress(contact.email)) {
    throw new Error('the server returned a contact without an id or valid email');
  }
  let name = contact.name;
  let nameProperty = 'FN';
  if (name === '') {
    name = contact.email;
  } else if (name === contact.email) {
    // Keeps a name the user set to the address apart from a contact with no name.
    nameProperty += ';X-RESTMAIL-EXPLICIT-NAME=TRUE';
  }
  const lines = [
    'BEGIN:VCARD',
    'VERSION:4.0',
    `UID:${base}/api/v1/accounts/${accountId}/contacts/${contact.id}`,
    `${nameProperty}:${vcardText(name)}`,
    `EMAIL:${vcardText(contact.email)}`,
  ];
  if (contact.trust_level) lines.push(`X-RESTMAIL-TRUST-LEVEL:${vcardText(contact.trust_level)}`);
  if (contact.source) lines.push(`X-RESTMAIL-SOURCE:${vcardText(contact.source)}`);
  // The server's own timestamps are written as it sent them, so their full
  // stored precision survives; a JavaScript Date would keep only milliseconds.
  if (contact.created_at) {
    basicUTC(contact.created_at);
    lines.push(`X-RESTMAIL-CREATED-AT:${contact.created_at}`);
  }
  if (contact.updated_at) {
    lines.push(`REV:${basicUTC(contact.updated_at)}`, `X-RESTMAIL-UPDATED-AT:${contact.updated_at}`);
  }
  lines.push('END:VCARD');
  return lines.map(foldVCardLine).join('');
}

export interface ContactsFile {
  text: string;
  contacts: number;
  bytes: number;
}

/**
 * contactsVCardFile reads every contact, one page at a time, and writes them as
 * one file. A page that is empty but says more follow is an error, so a
 * misbehaving server cannot make the export loop forever.
 */
export async function contactsVCardFile(
  listPage: (offset: number, limit: number) => Promise<ContactsPage>,
  base: string,
  accountId: number,
): Promise<ContactsFile> {
  const cards: string[] = [];
  for (let offset = 0; ; ) {
    const page = await listPage(offset, CONTACTS_PAGE_SIZE);
    for (const contact of page.data) cards.push(contactVCard(contact, base, accountId));
    if (!page.pagination?.has_more) break;
    if (page.data.length === 0) throw new Error('the server returned an empty contacts page with more to follow');
    offset += page.data.length;
  }
  const text = cards.join('');
  return { text, contacts: cards.length, bytes: encoder.encode(text).length };
}

/** isVCardFile says whether a chosen file looks like an address book by its name or type. */
export function isVCardFile(file: { name: string; type: string }): boolean {
  return /\.(vcf|vcard)$/i.test(file.name) || /^text\/(x-)?vcard$/i.test(file.type);
}
