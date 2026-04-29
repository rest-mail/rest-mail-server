import { useState, useEffect } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Separator } from '@/components/ui/separator';
import {
  Calendar,
  MapPin,
  User,
  Users,
  Check,
  X,
  HelpCircle,
  Clock,
  AlertTriangle,
  History,
} from 'lucide-react';
import { toast } from 'sonner';
import * as api from '@/api/client';
import { useMailStore } from '@/stores/mailStore';
import type { CalendarEvent } from '@/types';

interface CalendarInviteProps {
  events: CalendarEvent[];
  messageId: number;
}

function formatEventDate(dtstart: string, dtend: string, allDay: boolean): string {
  if (!dtstart) return '';

  const start = new Date(dtstart);
  const end = dtend ? new Date(dtend) : null;

  if (allDay) {
    const dateStr = start.toLocaleDateString([], {
      weekday: 'long',
      year: 'numeric',
      month: 'long',
      day: 'numeric',
    });
    if (end) {
      const endDate = new Date(end);
      // All-day end dates are exclusive in iCal, so subtract one day for display
      endDate.setDate(endDate.getDate() - 1);
      if (endDate.getTime() > start.getTime()) {
        return `${dateStr} - ${endDate.toLocaleDateString([], {
          weekday: 'long',
          year: 'numeric',
          month: 'long',
          day: 'numeric',
        })}`;
      }
    }
    return dateStr;
  }

  const startStr = start.toLocaleString([], {
    weekday: 'short',
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });

  if (end) {
    const sameDay = start.toDateString() === end.toDateString();
    if (sameDay) {
      const endTimeStr = end.toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
      });
      return `${startStr} - ${endTimeStr}`;
    }
    const endStr = end.toLocaleString([], {
      weekday: 'short',
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
    return `${startStr} - ${endStr}`;
  }

  return startStr;
}

function formatDuration(dtstart: string, dtend: string): string | null {
  if (!dtstart || !dtend) return null;
  const start = new Date(dtstart);
  const end = new Date(dtend);
  const diffMs = end.getTime() - start.getTime();
  if (diffMs <= 0) return null;

  const hours = Math.floor(diffMs / (1000 * 60 * 60));
  const minutes = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));

  if (hours === 0) return `${minutes}m`;
  if (minutes === 0) return `${hours}h`;
  return `${hours}h ${minutes}m`;
}

function methodBadge(method: string, status: string) {
  if (method === 'CANCEL' || status === 'CANCELLED') {
    return <Badge variant="destructive">Cancelled</Badge>;
  }
  if (method === 'REPLY') {
    return <Badge variant="secondary">Reply</Badge>;
  }
  if (method === 'REQUEST') {
    return <Badge variant="default">Invitation</Badge>;
  }
  return <Badge variant="outline">{method || 'Event'}</Badge>;
}

function partStatBadge(partstat: string) {
  switch (partstat) {
    case 'ACCEPTED':
      return <Badge variant="default" className="bg-green-600"><Check className="w-3 h-3" /> Accepted</Badge>;
    case 'DECLINED':
      return <Badge variant="destructive"><X className="w-3 h-3" /> Declined</Badge>;
    case 'TENTATIVE':
      return <Badge variant="secondary"><HelpCircle className="w-3 h-3" /> Tentative</Badge>;
    case 'NEEDS-ACTION':
      return <Badge variant="outline">Pending</Badge>;
    default:
      return null;
  }
}

export function CalendarInvite({ events, messageId }: CalendarInviteProps) {
  const [respondingAs, setRespondingAs] = useState<string | null>(null);
  const [responded, setResponded] = useState<string | null>(null);
  const [versionInfo, setVersionInfo] = useState<{
    isSuperseded: boolean;
    isCancelledByUpdate: boolean;
    latestSequence: number;
    versionCount: number;
  } | null>(null);
  const { accounts, activeAccountId } = useMailStore();

  // Display the first event (most common case). Computed before any early
  // return so hooks below see a stable signature on every render.
  const event = events?.[0];

  // Check if this event has been superseded by a newer version. Hook must run
  // on every render (no early return above it) to satisfy rules-of-hooks; the
  // body short-circuits when there's no event.
  useEffect(() => {
    if (!event?.uid || !activeAccountId) return;
    api.getCalendarEvents(activeAccountId).then(res => {
      const match = res.data?.find((e: { uid: string }) => e.uid === event.uid);
      if (match && (match.sequence > event.sequence || match.is_cancelled)) {
        setVersionInfo({
          isSuperseded: match.sequence > event.sequence,
          isCancelledByUpdate: match.is_cancelled && event.method !== 'CANCEL',
          latestSequence: match.sequence,
          versionCount: match.versions,
        });
      }
    }).catch(() => { /* silently ignore - feature enhancement only */ });
  }, [event?.uid, event?.sequence, event?.method, activeAccountId]);

  if (!events || events.length === 0 || !event) return null;

  const handleRespond = async (response: 'ACCEPTED' | 'DECLINED' | 'TENTATIVE') => {
    setRespondingAs(response);
    try {
      const activeAccount = accounts.find(a => a.id === activeAccountId);
      const fromAddress = activeAccount?.address || accounts[0]?.address;
      if (!fromAddress) {
        toast.error('No account address available to send reply');
        return;
      }

      await api.respondToCalendar(messageId, {
        response,
        from: fromAddress,
      });

      setResponded(response);
      const label = response.charAt(0) + response.slice(1).toLowerCase();
      toast.success(`Calendar invite ${label.toLowerCase()}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to send calendar response');
    } finally {
      setRespondingAs(null);
    }
  };

  const isCancelled = event.method === 'CANCEL' || event.status === 'CANCELLED';
  const isReply = event.method === 'REPLY';
  const isRequest = event.method === 'REQUEST';
  const duration = formatDuration(event.dtstart, event.dtend);

  return (
    <Card className="mb-4 py-4">
      <CardHeader className="pb-2 pt-0">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-2">
            <Calendar className="w-5 h-5 text-primary shrink-0" />
            <CardTitle className="text-base">{event.summary || 'Calendar Event'}</CardTitle>
          </div>
          {methodBadge(event.method, event.status ?? '')}
        </div>
      </CardHeader>
      <CardContent className="space-y-3 pt-0">
        {/* Date and time */}
        <div className="flex items-start gap-2 text-sm">
          <Clock className="w-4 h-4 text-muted-foreground mt-0.5 shrink-0" />
          <div>
            <p className="text-foreground">{formatEventDate(event.dtstart, event.dtend, event.all_day)}</p>
            {duration && !event.all_day && (
              <p className="text-muted-foreground text-xs">{duration}</p>
            )}
          </div>
        </div>

        {/* Location */}
        {event.location && (
          <div className="flex items-start gap-2 text-sm">
            <MapPin className="w-4 h-4 text-muted-foreground mt-0.5 shrink-0" />
            <p className="text-foreground">{event.location}</p>
          </div>
        )}

        {/* Organizer */}
        {event.organizer?.address && (
          <div className="flex items-start gap-2 text-sm">
            <User className="w-4 h-4 text-muted-foreground mt-0.5 shrink-0" />
            <p className="text-foreground">
              <span className="text-muted-foreground">Organizer: </span>
              {event.organizer.name ? `${event.organizer.name} (${event.organizer.address})` : event.organizer.address}
            </p>
          </div>
        )}

        {/* Description */}
        {event.description && (
          <>
            <Separator />
            <p className="text-sm text-muted-foreground whitespace-pre-wrap">{event.description}</p>
          </>
        )}

        {/* Attendees */}
        {event.attendees && event.attendees.length > 0 && (
          <>
            <Separator />
            <div className="flex items-start gap-2 text-sm">
              <Users className="w-4 h-4 text-muted-foreground mt-0.5 shrink-0" />
              <div className="space-y-1 flex-1">
                <p className="text-muted-foreground text-xs font-medium">
                  {event.attendees.length} attendee{event.attendees.length !== 1 ? 's' : ''}
                </p>
                {event.attendees.map((attendee, i) => (
                  <div key={i} className="flex items-center gap-2 text-xs">
                    <span className="text-foreground">
                      {attendee.name || attendee.address}
                    </span>
                    {attendee.partstat && partStatBadge(attendee.partstat)}
                    {attendee.role === 'OPT-PARTICIPANT' && (
                      <span className="text-muted-foreground">(optional)</span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          </>
        )}

        {/* Superseded warning */}
        {versionInfo?.isSuperseded && !isCancelled && (
          <>
            <Separator />
            <div className="flex items-center gap-2 text-sm text-amber-600">
              <History className="w-4 h-4" />
              <p>This invite has been updated (version {event.sequence} → {versionInfo.latestSequence}). Check your inbox for the latest version.</p>
            </div>
          </>
        )}

        {/* Cancelled by update warning */}
        {versionInfo?.isCancelledByUpdate && !isCancelled && (
          <>
            <Separator />
            <div className="flex items-center gap-2 text-sm text-destructive">
              <AlertTriangle className="w-4 h-4" />
              <p>This event has been cancelled by the organizer.</p>
            </div>
          </>
        )}

        {/* Cancelled warning */}
        {isCancelled && (
          <>
            <Separator />
            <div className="flex items-center gap-2 text-sm text-destructive">
              <AlertTriangle className="w-4 h-4" />
              <p>This event has been cancelled.</p>
            </div>
          </>
        )}

        {/* Version count */}
        {versionInfo && versionInfo.versionCount > 1 && (
          <div className="text-xs text-muted-foreground pl-6">
            {versionInfo.versionCount} versions of this event received
          </div>
        )}

        {/* RSVP buttons -- only show for REQUEST method, non-cancelled, non-superseded events */}
        {isRequest && !isCancelled && !versionInfo?.isSuperseded && !versionInfo?.isCancelledByUpdate && (
          <>
            <Separator />
            <div className="flex items-center gap-2">
              {responded ? (
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-muted-foreground">You responded:</span>
                  {partStatBadge(responded)}
                </div>
              ) : (
                <>
                  <Button
                    size="sm"
                    variant="default"
                    className="bg-green-600 hover:bg-green-700"
                    onClick={() => handleRespond('ACCEPTED')}
                    disabled={respondingAs !== null}
                  >
                    <Check className="w-3.5 h-3.5 mr-1" />
                    {respondingAs === 'ACCEPTED' ? 'Sending...' : 'Accept'}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => handleRespond('TENTATIVE')}
                    disabled={respondingAs !== null}
                  >
                    <HelpCircle className="w-3.5 h-3.5 mr-1" />
                    {respondingAs === 'TENTATIVE' ? 'Sending...' : 'Tentative'}
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => handleRespond('DECLINED')}
                    disabled={respondingAs !== null}
                  >
                    <X className="w-3.5 h-3.5 mr-1" />
                    {respondingAs === 'DECLINED' ? 'Sending...' : 'Decline'}
                  </Button>
                </>
              )}
            </div>
          </>
        )}

        {/* For reply messages, show what the attendee responded */}
        {isReply && event.attendees && event.attendees.length > 0 && (
          <>
            <Separator />
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">Response from {event.attendees[0].name || event.attendees[0].address}:</span>
              {partStatBadge(event.attendees[0].partstat ?? '')}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
