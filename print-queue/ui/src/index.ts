// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
//
// `defineModule` is an identity function. It exists so an author gets the
// host's types at this call site without importing the host.
import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import Page from './page.svelte';
import DeviceTab from './device-tab.svelte';
import FileAction from './file-action';
import HowTo from './how-to.svelte';
import './styles.css';

export default defineModule((host) => {
  // Everything a component of this module needs is built here, from the host
  // and from nothing else. The host arrives before any component is mounted,
  // so it is held rather than threaded down.
  holdHost(host);

  // host.signal fires when the person who signed in leaves. Every request of
  // host.client ends with it. Pass it to a call that takes a signal, and stop
  // every timer and stream you start when it fires, for example:
  //
  //   const timer = setInterval(refresh, 5000);
  //   host.signal.addEventListener('abort', () => clearInterval(timer));
  //   await client.listThings({}, host.signal);

  // Every registration below is held to `contributes` in module.yaml: a name
  // the manifest does not declare is refused at run time and the app drops
  // this module. The generator wrote both from one answer, so they agree; keep
  // them in step by hand after that.
  host.register.page('print-queue', Page);
  host.register.deviceTab('print-queue', DeviceTab);
  host.register.fileAction('print-queue', FileAction);
  // How to send a file from PrusaSlicer 2, on this module's page under
  // Settings, Extensions.
  host.register.settings('how-to', HowTo);
});
