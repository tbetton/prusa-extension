// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import DeviceTab from './device-tab.svelte';
import './styles.css';

export default defineModule((host) => {
  holdHost(host);

  // Every registration is held to `contributes` in module.yaml.
  host.register.deviceTab('filament-profile', DeviceTab);
});
