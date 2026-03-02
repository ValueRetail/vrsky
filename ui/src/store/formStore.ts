/**
 * Form Store
 * Manages wizard form state
 */

import { create } from 'zustand'
import type { ConnectionFormData, FormValidationError } from '../types/forms'

interface FormStore {
  currentStep: number
  totalSteps: number
  formData: Partial<ConnectionFormData>
  errors: FormValidationError[]
  isSubmitting: boolean
  isValid: boolean

  // Actions
  setStep: (step: number) => void
  nextStep: () => void
  previousStep: () => void
  updateFormData: (data: Partial<ConnectionFormData>) => void
  updateFieldData: (step: string, data: unknown) => void
  addError: (field: string, message: string) => void
  removeError: (field: string) => void
  clearErrors: () => void
  setErrors: (errors: FormValidationError[]) => void
  setIsSubmitting: (isSubmitting: boolean) => void
  setIsValid: (isValid: boolean) => void
  reset: () => void

  // Helpers
  getStepData: (step: number) => unknown
  hasErrors: () => boolean
  getErrorForField: (field: string) => string | undefined
}

const TOTAL_STEPS = 6 // BasicInfo, Source, Converter, Filter, Destination, Review

export const useFormStore = create<FormStore>((set, get) => ({
  currentStep: 0,
  totalSteps: TOTAL_STEPS,
  formData: {},
  errors: [],
  isSubmitting: false,
  isValid: false,

  setStep: (step) => {
    if (step >= 0 && step < TOTAL_STEPS) {
      set({ currentStep: step })
    }
  },

  nextStep: () => {
    const { currentStep, totalSteps } = get()
    if (currentStep < totalSteps - 1) {
      set({ currentStep: currentStep + 1 })
    }
  },

  previousStep: () => {
    const { currentStep } = get()
    if (currentStep > 0) {
      set({ currentStep: currentStep - 1 })
    }
  },

  updateFormData: (data) =>
    set((state) => ({
      formData: { ...state.formData, ...data },
    })),

  updateFieldData: (step, data) => {
    const { formData } = get()
    const stepKey = step.toLowerCase() as keyof ConnectionFormData
    set({
      formData: {
        ...formData,
        [stepKey]: data,
      },
    })
  },

  addError: (field, message) => {
    set((state) => ({
      errors: [
        ...state.errors.filter((e) => e.field !== field),
        { field, message },
      ],
    }))
  },

  removeError: (field) => {
    set((state) => ({
      errors: state.errors.filter((e) => e.field !== field),
    }))
  },

  clearErrors: () => set({ errors: [] }),

  setErrors: (errors) => set({ errors }),

  setIsSubmitting: (isSubmitting) => set({ isSubmitting }),

  setIsValid: (isValid) => set({ isValid }),

  reset: () =>
    set({
      currentStep: 0,
      formData: {},
      errors: [],
      isSubmitting: false,
      isValid: false,
    }),

  getStepData: (step) => {
    const { formData } = get()
    const stepNames = ['basicInfo', 'source', 'converter', 'filter', 'destination', 'review']
    const stepKey = stepNames[step] as keyof ConnectionFormData
    return formData[stepKey]
  },

  hasErrors: () => {
    const { errors } = get()
    return errors.length > 0
  },

  getErrorForField: (field) => {
    const { errors } = get()
    return errors.find((e) => e.field === field)?.message
  },
}))
