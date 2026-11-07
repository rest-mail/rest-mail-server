// The unsubscribe method a message's targets offer (#1575), in the order the webmail uses
// them: RFC 8058 one-click, which the server posts; a mailto: target, which the server sends
// from the account; or an HTTPS page, which the person opens. The server has already
// checked every target; nothing here sends anything.

export interface UnsubscribeTargets {
  one_click?: string;
  links: string[];
  mailto: { to: string; subject: string; body: string }[];
}

export type UnsubscribeChoice =
  | { method: 'one_click'; target: string; label: string; detail: string }
  | { method: 'mailto'; index: number; target: string; label: string; detail: string }
  | { method: 'link'; target: string; label: string; detail: string };

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** The method the Unsubscribe action uses, or null when the message offers none. */
export function unsubscribeChoice(targets: UnsubscribeTargets | null | undefined): UnsubscribeChoice | null {
  if (!targets) return null;
  if (targets.one_click) {
    return {
      method: 'one_click',
      target: targets.one_click,
      label: 'One-click unsubscribe',
      detail: `REST Mail will send one unsubscribe request to ${hostOf(targets.one_click)} for you.`,
    };
  }
  const mailto = targets.mailto[0];
  if (mailto) {
    return {
      method: 'mailto',
      index: 0,
      target: mailto.to,
      label: 'Unsubscribe by email',
      detail: `REST Mail will send an email from your account to ${mailto.to}${mailto.subject ? ` with the subject "${mailto.subject}"` : ''}.`,
    };
  }
  const link = targets.links[0];
  if (link) {
    return {
      method: 'link',
      target: link,
      label: 'Open the unsubscribe page',
      detail: `Your browser will open the sender's unsubscribe page on ${hostOf(link)}.`,
    };
  }
  return null;
}
