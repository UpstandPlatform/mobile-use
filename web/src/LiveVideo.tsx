import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Spinner } from '@/components/ui/spinner';

type MediaSource = 'screen' | 'camera' | 'microphone';
type LiveStreams = Partial<Record<MediaSource, MediaStream>>;
type MediaMode = 'off' | 'screen' | 'sensors' | 'all';

function gather(connection: RTCPeerConnection) {
  if (connection.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      connection.removeEventListener('icegatheringstatechange', check);
      reject(new Error('Media network candidates did not become available.'));
    }, 5500);
    function check() {
      if (connection.iceGatheringState !== 'complete') return;
      clearTimeout(timer);
      connection.removeEventListener('icegatheringstatechange', check);
      resolve();
    }
    connection.addEventListener('icegatheringstatechange', check);
    check();
  });
}

export function useLiveMedia(token: string, path: string, mode: MediaMode, recover = false) {
  const [streams, setStreams] = useState<LiveStreams>({});
  const [failure, setFailure] = useState('');
  const [attempt, setAttempt] = useState(0);
  const retryDelay = useRef(1500);

  useEffect(() => {
    setStreams({});
    setFailure('');
    if (mode === 'off') return;
    let active = true;
    let viewerID = '';
    let peer: RTCPeerConnection | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let startup: ReturnType<typeof setTimeout> | undefined;
    const disconnect = () => {
      clearTimeout(startup);
      if (viewerID) {
        void api(token, `${path}/${viewerID}`, 'DELETE').catch(() => undefined);
        viewerID = '';
      }
      const connection = peer;
      peer = undefined;
      connection?.close();
    };
    const fail = (error: unknown) => {
      if (!active) return;
      disconnect();
      setStreams({});
      setFailure(error instanceof Error ? error.message : 'The live media connection could not start.');
      const permanent = error instanceof ApiError && [401, 403, 404, 422].includes(error.status);
      if (recover && !permanent && !retry) {
        retry = setTimeout(() => setAttempt((value) => value + 1), retryDelay.current);
        retryDelay.current = Math.min(12000, retryDelay.current * 2);
      }
    };
    const receivers = new Map<MediaSource, {
      receiver: RTCRtpReceiver;
      stream: MediaStream;
      packets: number;
      lastPacket: number;
    }>();
    const show = (source: MediaSource, stream?: MediaStream) => {
      if (!active || !peer) return;
      setStreams((current) => current[source] === stream ? current : { ...current, [source]: stream });
    };
    const stats = setInterval(() => {
      const connection = peer;
      if (connection?.connectionState !== 'connected') return;
      for (const [source, observed] of receivers) {
        void observed.receiver.getStats().then((report) => {
          if (peer !== connection) return;
          for (const stat of report.values()) {
            if (stat.type !== 'inbound-rtp' || typeof stat.packetsReceived !== 'number') continue;
            if (stat.packetsReceived !== observed.packets) {
              observed.packets = stat.packetsReceived;
              observed.lastPacket = performance.now();
              show(source, observed.stream);
            } else if (performance.now() - observed.lastPacket > 2500) {
              show(source);
            }
          }
        }).catch(() => undefined);
      }
    }, 800);

    const start = async () => {
      try {
        const config = await api<{
          ice_servers: RTCIceServer[];
          ice_transport_policy: RTCIceTransportPolicy;
          sources?: MediaSource[];
        }>(token, path);
        if (!active) return;
        const sources = (config.sources ?? ['screen' as const]).filter((source) =>
          mode === 'all' || (mode === 'screen' ? source === 'screen' : source !== 'screen'),
        );
        if (!sources.length) return;
        const connection = new RTCPeerConnection({
          iceServers: config.ice_servers,
          iceTransportPolicy: config.ice_transport_policy,
        });
        peer = connection;
        const byTransceiver = new Map<RTCRtpTransceiver, MediaSource>();
        for (const source of sources) {
          const transceiver = connection.addTransceiver(source === 'microphone' ? 'audio' : 'video', { direction: 'recvonly' });
          byTransceiver.set(transceiver, source);
        }
        connection.ontrack = ({ receiver, track, transceiver }) => {
          const source = byTransceiver.get(transceiver);
          if (!active || peer !== connection || !source) return;
          const stream = new MediaStream([track]);
          const observed = { receiver, stream, packets: 0, lastPacket: performance.now() };
          receivers.set(source, observed);
          const unmute = () => {
            if (peer !== connection) return;
            observed.lastPacket = performance.now();
            show(source, stream);
          };
          track.onunmute = unmute;
          track.onmute = track.onended = () => { if (peer === connection) show(source); };
          if (!track.muted) unmute();
        };
        connection.onconnectionstatechange = () => {
          if (peer !== connection) return;
          if (connection.connectionState === 'connected') {
            clearTimeout(startup);
            retryDelay.current = 1500;
          } else if (connection.connectionState === 'failed') {
            fail(new Error('The live media connection was lost.'));
          }
        };
        await connection.setLocalDescription(await connection.createOffer());
        await gather(connection);
        if (!active || peer !== connection || !connection.localDescription) return;
        const response = await api<{ viewer_id: string; answer: RTCSessionDescriptionInit }>(
          token, path, 'POST', { ...connection.localDescription.toJSON(), sources: config.sources ? sources : undefined },
        );
        viewerID = response.viewer_id;
        if (!active || peer !== connection) {
          disconnect();
          return;
        }
        startup = setTimeout(() => fail(new Error('The live media connection timed out.')), 10000);
        await connection.setRemoteDescription(response.answer);
      } catch (error) {
        fail(error);
      }
    };
    void start();
    return () => {
      active = false;
      clearInterval(stats);
      clearTimeout(retry);
      disconnect();
    };
  }, [attempt, mode, path, recover, token]);

  return { streams, failure };
}

export function MediaVideo({ stream, label }: { stream: MediaStream; label: string }) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const video = ref.current;
    if (!video) return;
    video.srcObject = stream;
    return () => { video.srcObject = null; };
  }, [stream]);
  return <video className="max-h-[min(72vh,56rem)] max-w-full rounded-md object-contain"
    ref={ref} aria-label={label} autoPlay muted playsInline />;
}

export function LiveVideo({ token, path }: { token: string; path: string }) {
  const [timedOut, setTimedOut] = useState(false);
  const { streams, failure } = useLiveMedia(token, path, timedOut ? 'off' : 'screen');
  useEffect(() => {
    if (streams.screen || failure || timedOut) return;
    const timer = setTimeout(() => setTimedOut(true), 25000);
    return () => clearTimeout(timer);
  }, [failure, streams.screen, timedOut]);
  if (streams.screen) return <MediaVideo stream={streams.screen} label="Live phone video" />;
  if (failure || timedOut) return (
    <Alert variant="destructive" className="max-w-lg">
      <AlertDescription>{failure || 'The phone did not start sending video. Screen captures are still available.'}</AlertDescription>
    </Alert>
  );
  return <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status"><Spinner />Connecting to the phone…</div>;
}

function MicrophonePlayer({ stream }: { stream: MediaStream }) {
  const ref = useRef<HTMLAudioElement>(null);
  const [listening, setListening] = useState(false);
  const [failure, setFailure] = useState('');

  useEffect(() => {
    const audio = ref.current;
    if (!audio) return;
    audio.srcObject = stream;
    return () => { audio.srcObject = null; };
  }, [stream]);

  async function toggle() {
    const audio = ref.current;
    if (!audio) return;
    if (listening) {
      audio.pause();
      setListening(false);
    } else {
      try {
        await audio.play();
        setListening(true);
        setFailure('');
      } catch {
        setFailure('The browser could not play the microphone stream.');
      }
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <audio ref={ref} aria-label="Live phone microphone" />
      <Button type="button" variant="outline" onClick={() => void toggle()} aria-pressed={listening}>
        {listening ? 'Mute microphone playback' : 'Listen to microphone'}
      </Button>
      {failure && <span role="status" className="text-sm text-muted-foreground">{failure}</span>}
    </div>
  );
}

export function CompanionSensors({ camera, microphone, supported, paused, streams, failure }: {
  camera: boolean;
  microphone: boolean;
  supported: boolean;
  paused: boolean;
  streams: LiveStreams;
  failure: string;
}) {
  if (!camera && !microphone) return null;
  return (
    <Card>
      <CardHeader>
        <CardTitle>{camera && microphone ? 'Camera and microphone' : camera ? 'Camera' : 'Microphone'}</CardTitle>
        {paused && <CardDescription>Session paused</CardDescription>}
        {!paused && !supported && <CardDescription>Live camera and microphone sharing requires the updated companion app.</CardDescription>}
      </CardHeader>
      {!paused && supported && (
        <CardContent className="grid gap-3">
          {failure && <Alert><AlertDescription>{failure}</AlertDescription></Alert>}
          {camera && (streams.camera
            ? <MediaVideo stream={streams.camera} label="Live phone camera" />
            : <p className="text-sm text-muted-foreground">Tap Share camera on the phone to start live video.</p>)}
          {microphone && (streams.microphone
            ? <MicrophonePlayer stream={streams.microphone} />
            : <p className="text-sm text-muted-foreground">Tap Share microphone on the phone to start live audio.</p>)}
        </CardContent>
      )}
    </Card>
  );
}
