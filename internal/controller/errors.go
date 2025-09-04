package controller

type promoteProgressingError struct{}

func (e promoteProgressingError) Error() string {
	return "error promoting progressing deployment"
}

type promotePreviewUpdateError struct{}

func (e promotePreviewUpdateError) Error() string {
	return "error promoting preview deployment"
}

type promoteDepromoteUpdateError struct{}

func (e promoteDepromoteUpdateError) Error() string {
	return "error promoting active deployment"
}

type deployServiceUpdateError struct{}

func (e deployServiceUpdateError) Error() string {
	return "error updating preview deployment"
}
