/**
 * What the app shows while it works out who is signed in: the mark, the
 * wordmark, and a bar that sweeps until the session check answers. Takes the
 * whole viewport so nothing flashes underneath, and colours itself from the
 * active theme like every other screen.
 */
export function SplashScreen() {
  return (
    <div className="h-full flex flex-col items-center justify-center gap-4 bg-background">
      <img
        src={`${import.meta.env.BASE_URL}favicon.svg`}
        alt=""
        className="w-14 h-14 splash-rise"
      />
      <div className="flex flex-col items-center gap-1 splash-rise-late">
        <h1 className="font-heading-oswald text-2xl font-bold tracking-wider text-foreground">
          REST MAIL
        </h1>
        <p className="font-mono text-[11px] text-muted-foreground">
          {'// opening_your_mail'}
        </p>
      </div>
      <div className="mt-2 h-0.5 w-28 rounded-full bg-muted overflow-hidden">
        <div className="splash-sweep h-full w-1/3 rounded-full bg-primary" />
      </div>
    </div>
  );
}
