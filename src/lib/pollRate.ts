/** How often the state is read while nothing special is going on. */
export const POLL_MS = 5000
/** How often it is read while something is expected to change: an agent waiting for approval, or a change just made in a cluster. */
export const FAST_MS = 2000
/** How long after a change in a cluster (a collector resumed, telemetry turned on) the faster rate lasts. */
export const BOOST_MS = 120_000

export const pollEvery = (waiting: boolean, boosted: boolean) => (waiting || boosted ? FAST_MS : POLL_MS)
