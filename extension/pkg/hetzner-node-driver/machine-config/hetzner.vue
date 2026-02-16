<script>
import Loading from '@shell/components/Loading';
import CreateEditView from '@shell/mixins/create-edit-view';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import { Checkbox } from '@components/Form/Checkbox';
import { LabeledInput } from '@components/Form/LabeledInput';
import { RadioGroup } from '@components/Form/Radio';
import { NORMAN } from '@shell/config/types';
import { stringify, exceptionToErrorsArray } from '@shell/utils/error';
import { Banner } from '@components/Banner';

export default {
  components: {
    Loading, LabeledSelect, Checkbox, LabeledInput, RadioGroup, Banner
  },

  mixins: [CreateEditView],

  props: {
    credentialId: {
      type:     String,
      required: true,
    },

    cluster: {
      type:    Object,
      default: () => ({})
    },

    disabled: {
      type:    Boolean,
      default: false
    },
  },

  async fetch() {
    this.errors = [];

    try {
      if (this.credentialId) {
        this.credential = await this.$store.dispatch('rancher/find', {
          type: NORMAN.CLOUD_CREDENTIAL,
          id:   this.credentialId
        });
      }
    } catch (e) {
      this.credential = null;
    }

    // Auto-populate cluster ID for shared firewall and resource labeling
    const clusterName = this.cluster?.metadata?.name;

    if (clusterName && !this.value.clusterId) {
      this.value.clusterId = clusterName;
    }

    try {
      // Fetch locations
      this.locationOptions = await this.$store.dispatch('hetzner/locationOptions', { credentialId: this.credentialId });

      let defaultLocation = 'fsn1';

      if (!this.locationOptions.find((x) => x.value === defaultLocation)) {
        defaultLocation = this.locationOptions[0]?.value;
      }

      if (!this.value.serverLocation) {
        this.value.serverLocation = defaultLocation;
      }

      // Fetch server types
      this.serverTypeOptions = await this.$store.dispatch('hetzner/serverTypeOptions', { credentialId: this.credentialId });

      let defaultServerType = 'cx23';

      if (!this.serverTypeOptions.find((x) => x.value === defaultServerType)) {
        defaultServerType = this.serverTypeOptions.find((x) => x.memoryGb >= 4)?.value;

        if (!defaultServerType) {
          defaultServerType = this.serverTypeOptions[0]?.value;
        }
      }

      if (!this.value.serverType) {
        this.value.serverType = defaultServerType;
      }

      // Fetch images matching the selected server type's architecture
      const selectedType = this.serverTypeOptions.find((x) => x.value === this.value.serverType);
      const arch = selectedType?.architecture || 'x86';

      this.imageOptions = await this.$store.dispatch('hetzner/imageOptions', { credentialId: this.credentialId, architecture: arch });

      let defaultImage = 'ubuntu-24.04';

      if (!this.imageOptions.find((x) => x.value === defaultImage)) {
        defaultImage = this.imageOptions[0]?.value;
      }

      if (!this.value.image) {
        this.value.image = defaultImage;
      }

      // Fetch networks
      this.networkOptions = await this.$store.dispatch('hetzner/networkOptions', { credentialId: this.credentialId });

      // Fetch firewalls
      this.firewallOptions = await this.$store.dispatch('hetzner/firewallOptions', { credentialId: this.credentialId });

      // Fetch SSH keys
      this.sshKeyOptions = await this.$store.dispatch('hetzner/sshKeyOptions', { credentialId: this.credentialId });
    } catch (e) {
      console.error('Hetzner machine-config fetch error:', e);
      this.errors = exceptionToErrorsArray(e);
    }
  },

  data() {
    // Determine initial firewall mode from existing values
    let firewallMode = 'none';

    if (this.value?.createFirewall) {
      firewallMode = 'create';
    } else if (this.value?.firewalls && this.value.firewalls.length > 0) {
      firewallMode = 'existing';
    }

    return {
      credential:        null,
      locationOptions:   [],
      serverTypeOptions: [],
      imageOptions:      [],
      networkOptions:    [],
      firewallOptions:   [],
      sshKeyOptions:     [],
      useExistingSshKey: !!this.value?.existingSshKey,
      firewallMode,
      errors:            [],
    };
  },

  watch: {
    'credentialId'() {
      this.$fetch();
    },

    'cluster.metadata.name'(name) {
      if (name && !this.value.clusterId) {
        this.value.clusterId = name;
      }
    },

    useExistingSshKey(val) {
      if (!val) {
        this.value.existingSshKey = '';
      }
    },

    firewallMode(val) {
      if (val === 'none') {
        this.value.firewalls = [];
        this.value.createFirewall = false;
        this.value.firewallName = '';
        this.value.autoCreateFirewallRules = false;
      } else if (val === 'existing') {
        this.value.createFirewall = false;
        this.value.firewallName = '';
        this.value.autoCreateFirewallRules = false;
      } else if (val === 'create') {
        this.value.firewalls = [];
        this.value.createFirewall = true;
      }
    },

    'value.serverType'(newVal) {
      if (newVal && this.serverTypeOptions.length) {
        const selected = this.serverTypeOptions.find((x) => x.value === newVal);

        if (selected) {
          this.refreshImages(selected.architecture);
        }
      }
    },
  },

  methods: {
    stringify,

    async refreshImages(architecture) {
      try {
        this.imageOptions = await this.$store.dispatch('hetzner/imageOptions', {
          credentialId: this.credentialId,
          architecture: architecture || 'x86',
        });

        // Reset selected image if it's no longer available for this architecture
        if (this.value.image && !this.imageOptions.find((x) => x.value === this.value.image)) {
          this.value.image = this.imageOptions[0]?.value || '';
        }
      } catch (e) {
        console.error('Error refreshing images for architecture:', architecture, e);
      }
    },
  },
};
</script>

<template>
  <Loading
    v-if="$fetchState.pending"
    :delayed="true"
  />
  <div v-else-if="errors.length">
    <div
      v-for="(err, idx) in errors"
      :key="idx"
    >
      <Banner
        color="error"
        :label="stringify(err)"
      />
    </div>
  </div>
  <div v-else>
    <div class="row mt-20">
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.serverLocation"
          :mode="mode"
          :options="locationOptions"
          :searchable="true"
          :required="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.location.label')"
        />
      </div>
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.serverType"
          :mode="mode"
          :options="serverTypeOptions"
          :searchable="true"
          :required="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.serverType.label')"
        />
      </div>
    </div>

    <div class="row mt-20">
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.image"
          :mode="mode"
          :options="imageOptions"
          :searchable="true"
          :required="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.image.label')"
        />
      </div>
      <div class="col span-6 pt-5">
        <h3>{{ t('cluster.machineConfig.hetzner.additionalOptions.label') }}</h3>
        <Checkbox
          v-model:value="value.usePrivateNetwork"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.usePrivateNetwork.label')"
        />
        <Checkbox
          v-model:value="value.disablePublicIpv4"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.disablePublicIpv4.label')"
        />
        <Checkbox
          v-model:value="value.disablePublicIpv6"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.disablePublicIpv6.label')"
        />
      </div>
    </div>

    <div
      v-if="value.usePrivateNetwork"
      class="row mt-20"
    >
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.networks"
          :mode="mode"
          :options="networkOptions"
          :searchable="true"
          :multiple="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.networks.label')"
        />
      </div>
    </div>

    <!-- Firewall Section -->
    <div class="row mt-20">
      <div class="col span-12">
        <h3>{{ t('cluster.machineConfig.hetzner.firewallSection.label') }}</h3>
        <RadioGroup
          v-model:value="firewallMode"
          name="firewallMode"
          :disabled="disabled"
          :options="[
            { value: 'none', label: t('cluster.machineConfig.hetzner.firewallMode.none') },
            { value: 'existing', label: t('cluster.machineConfig.hetzner.firewallMode.existing') },
            { value: 'create', label: t('cluster.machineConfig.hetzner.firewallMode.create') },
          ]"
          :row="true"
        />
      </div>
    </div>

    <!-- Existing Firewall Selection -->
    <div
      v-if="firewallMode === 'existing'"
      class="row mt-10"
    >
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.firewalls"
          :mode="mode"
          :options="firewallOptions"
          :searchable="true"
          :multiple="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.firewalls.label')"
        />
      </div>
    </div>

    <!-- Create New Firewall -->
    <div
      v-if="firewallMode === 'create'"
      class="row mt-10"
    >
      <div class="col span-6">
        <LabeledInput
          v-model:value="value.firewallName"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.firewallName.label')"
          :placeholder="t('cluster.machineConfig.hetzner.firewallName.placeholder')"
        />
      </div>
      <div class="col span-6 pt-5">
        <Checkbox
          v-model:value="value.autoCreateFirewallRules"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.autoCreateFirewallRules.label')"
        />
        <p class="text-muted mt-5">
          {{ t('cluster.machineConfig.hetzner.autoCreateFirewallRules.description') }}
        </p>
      </div>
    </div>

    <div
      v-if="firewallMode === 'create' && value.autoCreateFirewallRules && value.disablePublicIpv4"
      class="row mt-10"
    >
      <div class="col span-12">
        <Banner
          color="warning"
          :label="t('cluster.machineConfig.hetzner.autoCreateFirewallRules.ipv4Warning')"
        />
      </div>
    </div>

    <div class="row mt-20">
      <div class="col span-6 pt-5">
        <Checkbox
          v-model:value="useExistingSshKey"
          :mode="mode"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.useExistingSshKey.label')"
        />
      </div>
    </div>

    <div
      v-if="useExistingSshKey"
      class="row mt-10"
    >
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.existingSshKey"
          :mode="mode"
          :options="sshKeyOptions"
          :searchable="true"
          :disabled="disabled"
          :label="t('cluster.machineConfig.hetzner.existingSshKey.label')"
        />
      </div>
    </div>
  </div>
</template>
