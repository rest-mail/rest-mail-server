import { useCallback } from 'react';
import { mailPlatform } from '../platform';

export function useNotifications() {
  const requestPermission = useCallback(async () => {
    if (mailPlatform().nativeNotifications) return;
    if (!('Notification' in window)) return;
    if (Notification.permission === 'default') {
      await Notification.requestPermission();
    }
  }, []);

  const showDesktopNotification = useCallback((title: string, body: string) => {
    if (mailPlatform().nativeNotifications) return;
    if (!('Notification' in window)) return;
    if (Notification.permission !== 'granted') return;
    const icon = `${import.meta.env.BASE_URL}favicon.svg`;
    new Notification(title, { body, icon });
  }, []);

  return { requestPermission, showDesktopNotification };
}
