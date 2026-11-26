import { useEffect, useRef, useState } from 'react';
import { api } from './api';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Spinner } from '@/components/ui/spinner';

function gather(connection: RTCPeerConnection) {
  if (connection.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      connection.removeEventListener('icegatheringstatechange', check);
      reject(new Error('Video network candidates did not become available.'));
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

export function LiveVideo({ token, path }: { token: string; path: string }) {
  const screen = useRef<HTMLVideoElement | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [failure, setFailure] = useState('');

  useEffect(() => {
    let active = true;
    let peer: RTCPeerConnection | undefined;
    let viewerID = '';
    const video = screen.current;
    const startup = setTimeout(() => {
      if (active && video && video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) {
        setFailure('The phone did not start sending video. Screen captures are still available.');
        peer?.close();
      }
    }, 25000);

    const disconnect = () => {
      if (viewerID) void api(token, `${path}/${viewerID}`, 'DELETE').catch(() => undefined);
    };

    const start = async () => {
      try {
        const config = await api<{
          ice_servers: RTCIceServer[];
          ice_transport_policy: RTCIceTransportPolicy;
        }>(token, path);
        if (!active) return;
        peer = new RTCPeerConnection({
          iceServers: config.ice_servers,
          iceTransportPolicy: config.ice_transport_policy,
        });
        peer.addTransceiver('video', { direction: 'recvonly' });
        peer.ontrack = ({ streams, track }) => {
          if (active && video) video.srcObject = streams[0] ?? new MediaStream([track]);
        };
        peer.onconnectionstatechange = () => {
          if (active && peer?.connectionState === 'failed') setFailure('The live video connection was lost.');
        };
        await peer.setLocalDescription(await peer.createOffer());
        await gather(peer);
        if (!active || !peer.localDescription) return;
        const response = await api<{ viewer_id: string; answer: RTCSessionDescriptionInit }>(
          token,
          path,
          'POST',
          peer.localDescription.toJSON(),
        );
        viewerID = response.viewer_id;
        if (!active) {
          disconnect();
          return;
        }
        await peer.setRemoteDescription(response.answer);
      } catch (error) {
        if (!active) return;
        setFailure(error instanceof Error ? error.message : 'The live video connection could not start.');
        disconnect();
        peer?.close();
      }
    };
    void start();
    return () => {
      active = false;
      clearTimeout(startup);
      disconnect();
      peer?.close();
      if (video) video.srcObject = null;
    };
  }, [token, path]);

  return (
    <div className="grid min-h-64 w-full place-items-center gap-3">
      {!loaded && !failure && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
          <Spinner />
          Connecting to the phone…
        </div>
      )}
      {failure && (
        <Alert variant="destructive" className="max-w-lg">
          <AlertDescription>{failure}</AlertDescription>
        </Alert>
      )}
      <video
        className="max-h-[min(72vh,56rem)] max-w-full rounded-md object-contain"
        ref={screen}
        hidden={!loaded}
        aria-label="Live phone video"
        autoPlay
        muted
        playsInline
        onLoadedData={() => setLoaded(true)}
      />
    </div>
  );
}
