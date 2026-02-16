<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import { LabeledInput } from '@components/Form/LabeledInput';
import FormValidation from '@shell/mixins/form-validation';

export default {
  emits: ['validationChanged', 'valueChanged'],

  components: { LabeledInput },
  mixins:     [CreateEditView, FormValidation],

  data() {
    return {
      fvFormRuleSets: [
        { path: 'decodedData.apiToken', rules: ['required'] }
      ]
    };
  },

  watch: {
    fvFormIsValid(newValue) {
      this.$emit('validationChanged', !!newValue);
    }
  },

  methods: {
    async test() {
      try {
        await this.$store.dispatch('hetzner/request', {
          token:   this.value.decodedData.apiToken,
          command: 'locations'
        });

        return true;
      } catch (e) {
        return false;
      }
    }
  }
};
</script>

<template>
  <div>
    <LabeledInput
      :value="value.decodedData.apiToken"
      label-key="cluster.credential.hetzner.apiToken.label"
      placeholder-key="cluster.credential.hetzner.apiToken.placeholder"
      type="password"
      :mode="mode"
      :required="true"
      :rules="fvGetAndReportPathRules('decodedData.apiToken')"
      @update:value="$emit('valueChanged', 'apiToken', $event)"
    />
    <p
      v-clean-html="t('cluster.credential.hetzner.apiToken.help', {}, true)"
      class="text-muted mt-10"
    />
  </div>
</template>
