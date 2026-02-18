export interface User {
  id: number;
  email: string;
  display_name: string;
}

export interface LoginResponse {
  data: {
    access_token: string;
    expires_in: number;
    user: User;
  };
}

export interface Folder {
  name: string;
  total: number;
  unread: number;
}

export interface MessageSummary {
  id: number;
  mailbox_id: number;
  folder: string;
  message_id: string;
  sender: string;
  sender_name: string;
  recipients_to: string | null;
  subject: string;
  size_bytes: number;
  has_attachments: boolean;
  is_read: boolean;
  is_flagged: boolean;
  is_starred: boolean;
  is_draft: boolean;
  received_at: string;
}

export interface MessageDetail extends MessageSummary {
  body_text: string;
  body_html: string;
  headers: Record<string, string> | null;
  in_reply_to: string;
  references: string;
  thread_id: string;
}

export interface Account {
  id: number;
  address: string;
  display_name: string;
  is_primary: boolean;
  mailbox_id: number;
}

export interface Attachment {
  id: number;
  filename: string;
  content_type: string;
  size_bytes: number;
}

export interface Pagination {
  cursor: string;
  has_more: boolean;
  total: number;
}
